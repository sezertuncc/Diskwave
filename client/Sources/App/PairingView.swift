import SwiftUI

struct PairingView: View {
    @StateObject private var vm = PairingViewModel()

    var body: some View {
        VStack(spacing: 0) {

            // ── Top Bar ──────────────────────────────────────────────────
            HStack {
                Spacer()
                Menu {
                    Button {
                        NSApplication.shared.terminate(nil)
                    } label: {
                        Label("Quit Diskwave", systemImage: "xmark.circle")
                    }
                } label: {
                    Image(systemName: "ellipsis")
                        .font(.system(size: 12, weight: .medium))
                        .foregroundColor(Color(.secondaryLabelColor))
                        .frame(width: 26, height: 26)
                        .background(Color(.controlBackgroundColor))
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                        .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color(.separatorColor), lineWidth: 1))
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
            }
            .padding(.horizontal, 16)
            .padding(.top, 12)

            // ── Logo ─────────────────────────────────────────────────────
            Image("Logo")
                .resizable()
                .scaledToFit()
                .frame(height: 38)
                .padding(.top, 8)
                .padding(.bottom, 16)

            // ── Form ─────────────────────────────────────────────────────
            VStack(spacing: 12) {
                PlainField(
                    placeholder: "Server address",
                    text: $vm.serverAddress,
                    icon: "server.rack"
                )
                .autocorrectionDisabled()

                PairingCodeField(code: $vm.pairingCode)
            }
            .padding(.horizontal, 20)

            // ── Error ─────────────────────────────────────────────────────
            if let error = vm.errorMessage {
                Text(error)
                    .font(.system(size: 11))
                    .foregroundColor(.dsDestructive)
                    .padding(.top, 10)
                    .padding(.horizontal, 20)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .transition(.opacity.combined(with: .move(edge: .top)))
            }

            Spacer(minLength: 16)

            // ── Connect ────────────────────────────────────────────────────
            VStack(spacing: 8) {
                ConnectButton(
                    loading: vm.isConnecting,
                    enabled: vm.pairingCode.count == 6 && !vm.serverAddress.isEmpty
                ) {
                    Task { await vm.connect() }
                }

                Text("Run  diskwave pair-code  on your server")
                    .font(.system(size: 10))
                    .foregroundColor(Color(.secondaryLabelColor))
                    .multilineTextAlignment(.center)
            }
            .padding(.horizontal, 20)
            .padding(.bottom, 20)
        }
        .frame(width: 320, height: 340)
        .background(Color(.windowBackgroundColor))
    }
}

// MARK: - Plain field

private struct PlainField: View {
    let placeholder: String
    @Binding var text: String
    var icon: String? = nil

    var body: some View {
        HStack(spacing: 8) {
            if let icon {
                Image(systemName: icon)
                    .font(.system(size: 12))
                    .foregroundColor(Color(.tertiaryLabelColor))
                    .frame(width: 16)
            }
            TextField(placeholder, text: $text)
                .font(.system(size: 13))
                .textFieldStyle(.plain)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 9)
        .background(Color(.controlBackgroundColor))
        .clipShape(RoundedRectangle(cornerRadius: 8))
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(Color(.separatorColor), lineWidth: 1)
        )
    }
}

// MARK: - Connect button

private struct ConnectButton: View {
    let loading: Bool
    let enabled: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                if loading {
                    ProgressView()
                        .progressViewStyle(.circular)
                        .scaleEffect(0.65)
                        .tint(.white)
                }
                Text(loading ? "Connecting…" : "Connect")
                    .font(.system(size: 13, weight: .medium))
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 9)
            .background(enabled ? Color(hex: "#1A9AE0") : Color(hex: "#1A9AE0").opacity(0.35))
            .foregroundColor(.white)
            .clipShape(RoundedRectangle(cornerRadius: 8))
        }
        .buttonStyle(.plain)
        .disabled(!enabled || loading)
    }
}

// MARK: - ViewModel

@MainActor
final class PairingViewModel: ObservableObject {
    @Published var serverAddress = ""
    @Published var pairingCode = ""
    @Published var isConnecting = false
    @Published var errorMessage: String? = nil

    func connect() async {
        let cleanAddress = serverAddress.trimmingCharacters(in: .whitespacesAndNewlines)
        guard pairingCode.count == 6, !cleanAddress.isEmpty else { return }
        isConnecting = true
        errorMessage = nil
        do {
            let client = QUICClient()
            try await client.pair(host: cleanAddress, code: pairingCode)
            AppState.shared.setConnected(host: client.host, client: client)
        } catch {
            withAnimation { errorMessage = describe(error) }
        }
        isConnecting = false
    }

    private func describe(_ error: Error) -> String {
        if let e = error as? DiskWaveError {
            switch e {
            case .invalidCode:      return "Incorrect pairing code"
            case .connectionFailed: return "Could not reach server"
            case .timeout:          return "Connection timed out"
            }
        }
        return error.localizedDescription
    }
}

enum DiskWaveError: Error {
    case invalidCode
    case connectionFailed
    case timeout
}

#Preview { PairingView() }