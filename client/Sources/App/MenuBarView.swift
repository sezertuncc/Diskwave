import SwiftUI

struct MenuBarView: View {
    @ObservedObject var state = AppState.shared

    var body: some View {
        if state.isConnected {
            ConnectedView()
        } else {
            PairingView()
        }
    }
}

// MARK: - Menu Bar Icon

struct MenuBarIcon: View {
    @ObservedObject var state = AppState.shared
    @State private var pulse = false

    var body: some View {
        ZStack(alignment: .topTrailing) {
            Image(systemName: "externaldrive.fill")
                .font(.system(size: 14, weight: .medium))

            Circle()
                .fill(statusColor)
                .frame(width: 6, height: 6)
                .overlay(
                    Circle()
                        .stroke(statusColor.opacity(0.4), lineWidth: 2)
                        .scaleEffect(pulse ? 1.8 : 1)
                        .opacity(pulse ? 0 : 1)
                )
                .offset(x: 3, y: -3)
        }
        .onAppear {
            withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: false)) {
                pulse = true
            }
        }
    }

    private var statusColor: Color {
        switch state.connectionStatus {
        case .connected:    return .dsSuccess
        case .connecting:   return .dsPrimary
        case .disconnected: return .dsSecondary
        }
    }
}