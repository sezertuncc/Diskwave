import SwiftUI
import AppKit

struct ConnectedView: View {
    @ObservedObject var state = AppState.shared
    @State private var launchAtLogin = LaunchAtLogin.isEnabled

    var body: some View {
        VStack(spacing: 0) {

            // ── Header ────────────────────────────────────────────────────
            HStack(spacing: 10) {
                Image("Logo")
                    .resizable()
                    .scaledToFit()
                    .frame(height: 28)

                HStack(spacing: 5) {
                    Circle()
                        .fill(state.connectionStatus == .connected ? Color(hex: "#34C759") : Color(.systemOrange))
                        .frame(width: 7, height: 7)
                    Text(state.connectionStatus == .connected ? "Connected" : "Connecting…")
                        .font(.system(size: 12, weight: .medium))
                        .foregroundColor(Color(.secondaryLabelColor))
                }

                Spacer()

                // ⋯ context menu
                Menu {
                    Button {
                        AppState.shared.disconnect()
                    } label: {
                        Label("Disconnect", systemImage: "eject.fill")
                    }

                    Divider()

                    Toggle(isOn: $launchAtLogin) {
                        Label("Launch at Login", systemImage: "power")
                    }
                    .onChange(of: launchAtLogin) { val in
                        LaunchAtLogin.isEnabled = val
                    }

                    Button {
                        NSApplication.shared.terminate(nil)
                    } label: {
                        Label("Quit Diskwave", systemImage: "xmark.circle")
                    }
                } label: {
                    Image(systemName: "ellipsis")
                        .font(.system(size: 12, weight: .medium))
                        .foregroundColor(Color(.secondaryLabelColor))
                        .frame(width: 28, height: 28)
                        .background(Color(.controlBackgroundColor))
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                        .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color(.separatorColor), lineWidth: 1))
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)

            Separator()

            // ── Mount path ────────────────────────────────────────────────
            VStack(spacing: 0) {
                HStack(spacing: 8) {
                    Image(systemName: "internaldrive.fill")
                        .font(.system(size: 11))
                        .foregroundColor(Color(hex: "#1A9AE0"))
                    Text("/Volumes/Diskwave")
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundColor(Color(.secondaryLabelColor))
                    Spacer()
                    // Mount status indicator
                    if state.isMounted {
                        Circle()
                            .fill(Color(hex: "#34C759"))
                            .frame(width: 6, height: 6)
                    } else if state.mountError != nil {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .font(.system(size: 10))
                            .foregroundColor(Color(.systemOrange))
                    } else {
                        ProgressView()
                            .progressViewStyle(.circular)
                            .scaleEffect(0.5)
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 9)
                .background(Color(.controlBackgroundColor).opacity(0.5))

                // Error row with retry
                if let err = state.mountError {
                    Separator()
                    HStack(spacing: 6) {
                        Image(systemName: "exclamationmark.circle.fill")
                            .font(.system(size: 10))
                            .foregroundColor(Color(.systemRed))
                        Text(err)
                            .font(.system(size: 10))
                            .foregroundColor(Color(.systemRed))
                            .lineLimit(2)
                        Spacer()
                        Button("Retry") {
                            AppState.shared.retryMount()
                        }
                        .font(.system(size: 10, weight: .medium))
                        .foregroundColor(Color(hex: "#1A9AE0"))
                        .buttonStyle(.plain)
                    }
                    .padding(.horizontal, 16)
                    .padding(.vertical, 6)
                    .background(Color(.systemRed).opacity(0.05))
                }
            }

            Separator()

            // ── Stats ─────────────────────────────────────────────────────
            VStack(spacing: 0) {
                StatRow(icon: "arrow.up.circle.fill",   iconColor: Color(hex: "#34C759"),  label: "Upload",   value: state.uploadSpeed)
                Separator().padding(.horizontal, 16)
                StatRow(icon: "arrow.down.circle.fill", iconColor: Color(hex: "#1A9AE0"),  label: "Download", value: state.downloadSpeed)
                Separator().padding(.horizontal, 16)
                StatRow(icon: "cylinder.fill",          iconColor: Color(hex: "#8B5CF6"),  label: "Used",     value: state.formattedUsedSpace)
            }

            Separator()

            // ── Server ────────────────────────────────────────────────────
            HStack(spacing: 6) {
                Image(systemName: "server.rack")
                    .font(.system(size: 11))
                    .foregroundColor(Color(.tertiaryLabelColor))
                Text(state.serverHost)
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundColor(Color(.secondaryLabelColor))
                Spacer()
                Text(":7879")
                    .font(.system(size: 11))
                    .foregroundColor(Color(.tertiaryLabelColor))
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 9)
        }
        .frame(width: 300)
        .background(Color(.windowBackgroundColor))
    }
}

// MARK: - Subviews

private struct Separator: View {
    var body: some View {
        Rectangle()
            .fill(Color(.separatorColor))
            .frame(height: 0.5)
    }
}

private struct StatRow: View {
    let icon: String
    let iconColor: Color
    let label: String
    let value: String

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .font(.system(size: 14))
                .foregroundColor(iconColor)
                .frame(width: 20)
            Text(label)
                .font(.system(size: 13))
                .foregroundColor(Color(.secondaryLabelColor))
            Spacer()
            Text(value)
                .font(.system(size: 13, weight: .medium))
                .foregroundColor(Color(.labelColor))
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 11)
    }
}