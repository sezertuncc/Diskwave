import Foundation
import AppKit
import ServiceManagement

// MARK: - SMBCredentials (matches server JSON)

struct SMBCredentials: Decodable {
    let port: Int
    let share: String
    let username: String
    let password: String
}

// MARK: - SMBMounter

final class SMBMounter {
    static let shared = SMBMounter()

    private let volumeName = "Diskwave"
    private var mountPoint: String { "/Volumes/\(volumeName)" }
    private(set) var currentMountPath: String?

    func mount(host: String, creds: SMBCredentials) async throws -> String {
        await unmount()

        let fm = FileManager.default
        if !fm.fileExists(atPath: mountPoint) {
            try fm.createDirectory(atPath: mountPoint, withIntermediateDirectories: true)
        }

        // Write /etc/nsmb.conf tuning before mount (requires root or existing file)
        applyNSMBConf()

        // //username:password@host:port/share
        let url = "//\(creds.username):\(creds.password)@\(host):\(creds.port)/\(creds.share)"

        // rwsize=8388608 → 8 MB read/write chunks (default is 1 MB, macOS cap is 8 MB)
        // soft             → don't hang forever on network drop
        // nostreams        → skip AppleDouble / named streams metadata — pure throughput
        // nolockd          → disable NFS locking daemon overhead
        let result = try await runProcess("/sbin/mount_smbfs", args: [
            "-N",
            "-o", "rwsize=8388608,soft,nostreams,nolockd",
            url,
            mountPoint
        ])

        guard result.exitCode == 0 else {
            try? fm.removeItem(atPath: mountPoint)
            throw MountError.mountFailed(result.stderr.isEmpty ? "mount_smbfs exited \(result.exitCode)" : result.stderr)
        }

        currentMountPath = mountPoint
        return mountPoint
    }

    // Writes performance tuning to /etc/nsmb.conf.
    // Runs without admin — only succeeds if the file is already writable or doesn't exist yet
    // (first-time setup with sudo, subsequent mounts read existing file).
    private func applyNSMBConf() {
        let conf = """
        [default]
        streams=no
        notify_off=yes
        smb_neg=smb2_only
        smb_neg=smb3_only
        dir_cache_max_cnt=0
        """
        let path = "/etc/nsmb.conf"
        // Only write if not already tuned by us
        if let existing = try? String(contentsOfFile: path, encoding: .utf8),
           existing.contains("notify_off=yes") { return }
        try? conf.write(toFile: path, atomically: true, encoding: .utf8)
    }

    func unmount() async {
        guard let path = currentMountPath else { return }
        currentMountPath = nil
        _ = try? await runProcess("/usr/bin/diskutil", args: ["unmount", "force", path])
        try? FileManager.default.removeItem(atPath: path)
    }

    // MARK: - Process helper

    private struct ProcessResult {
        let exitCode: Int32
        let stderr: String
    }

    private func runProcess(_ exe: String, args: [String]) async throws -> ProcessResult {
        try await withCheckedThrowingContinuation { cont in
            DispatchQueue.global(qos: .utility).async {
                let proc = Process()
                proc.executableURL = URL(fileURLWithPath: exe)
                proc.arguments = args
                let errPipe = Pipe()
                proc.standardError = errPipe
                do {
                    try proc.run()
                    proc.waitUntilExit()
                    let stderr = String(
                        data: errPipe.fileHandleForReading.readDataToEndOfFile(),
                        encoding: .utf8
                    )?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                    cont.resume(returning: ProcessResult(exitCode: proc.terminationStatus, stderr: stderr))
                } catch {
                    cont.resume(throwing: error)
                }
            }
        }
    }
}

// MARK: - LaunchAtLogin

struct LaunchAtLogin {
    static var isEnabled: Bool {
        get {
            if #available(macOS 13, *) {
                return SMAppService.mainApp.status == .enabled
            }
            return false
        }
        set {
            if #available(macOS 13, *) {
                do {
                    if newValue {
                        if SMAppService.mainApp.status != .enabled {
                            try SMAppService.mainApp.register()
                        }
                    } else {
                        try SMAppService.mainApp.unregister()
                    }
                } catch {
                    print("[DiskWave] LaunchAtLogin error: \(error)")
                }
            }
        }
    }
}

// MARK: - MountError

enum MountError: LocalizedError {
    case badURL
    case mountFailed(String)

    var errorDescription: String? {
        switch self {
        case .badURL:              return "Invalid server address"
        case .mountFailed(let m):  return "Mount failed: \(m)"
        }
    }
}