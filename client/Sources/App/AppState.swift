import SwiftUI
import Combine

enum ConnectionStatus {
    case disconnected
    case connecting
    case connected
}

@MainActor
final class AppState: ObservableObject {
    static let shared = AppState()

    @Published var isConnected = false
    @Published var connectionStatus: ConnectionStatus = .disconnected
    @Published var serverHost = ""
    @Published var usedBytes: Int64 = 0
    @Published var totalBytes: Int64 = 0
    @Published var uploadBytesPerSec: Double = 0
    @Published var downloadBytesPerSec: Double = 0
    @Published var isMounted: Bool = false
    @Published var mountError: String? = nil

    private(set) var client: QUICClient?
    private var statsTimer: Timer?

    var formattedUsedSpace: String {
        ByteCountFormatter.string(fromByteCount: usedBytes, countStyle: .file)
    }

    var uploadSpeed: String { formatSpeed(uploadBytesPerSec) }
    var downloadSpeed: String { formatSpeed(downloadBytesPerSec) }

    func setConnected(host: String, client: QUICClient) {
        self.serverHost = host
        self.client = client
        self.connectionStatus = .connected
        self.isConnected = true
        self.mountError = nil
        self.isMounted = false
        startStatsPolling()
        mountSMB(host: host, client: client)
    }

    func retryMount() {
        guard let client, isConnected else { return }
        mountError = nil
        mountSMB(host: serverHost, client: client)
    }

    private func mountSMB(host: String, client: QUICClient) {
        guard let creds = client.smbCredentials else {
            mountError = "SMB credentials not available — try reconnecting"
            return
        }
        Task.detached {
            do {
                let mountPath = try await SMBMounter.shared.mount(host: host, creds: creds, tunnelPort: client.smbLocalPort)
                await MainActor.run {
                    self.isMounted = true
                    self.mountError = nil
                    print("[DiskWave] Mounted at \(mountPath)")
                    if let url = URL(string: "file://\(mountPath)") {
                        NSWorkspace.shared.open(url)
                    }
                }
            } catch {
                await MainActor.run {
                    self.isMounted = false
                    self.mountError = error.localizedDescription
                    print("[DiskWave] SMB mount error: \(error)")
                }
            }
        }
    }

    func disconnect() {
        client?.disconnect()
        client = nil
        isConnected = false
        connectionStatus = .disconnected
        serverHost = ""
        usedBytes = 0
        uploadBytesPerSec = 0
        downloadBytesPerSec = 0
        isMounted = false
        mountError = nil
        stopStatsPolling()
        Task.detached { await SMBMounter.shared.unmount() }
    }

    func logout() {
        disconnect()
    }

    private func startStatsPolling() {
        statsTimer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.refreshStats() }
        }
    }

    private func stopStatsPolling() {
        statsTimer?.invalidate()
        statsTimer = nil
    }

    private func refreshStats() {
        guard let client else { return }
        uploadBytesPerSec = client.currentUploadSpeed
        downloadBytesPerSec = client.currentDownloadSpeed
    }

    private func formatSpeed(_ bytesPerSec: Double) -> String {
        if bytesPerSec < 1024 {
            return String(format: "%.0f B/s", bytesPerSec)
        } else if bytesPerSec < 1024 * 1024 {
            return String(format: "%.1f KB/s", bytesPerSec / 1024)
        } else {
            return String(format: "%.1f MB/s", bytesPerSec / (1024 * 1024))
        }
    }
}