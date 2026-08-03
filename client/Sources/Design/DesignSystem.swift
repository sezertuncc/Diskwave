import SwiftUI

// MARK: - Colors

extension Color {
    static let dsBackground   = Color(hex: "#FFFFFF")
    static let dsSurface      = Color(hex: "#F5F9FF")
    static let dsBorder       = Color(hex: "#DDE6F0")
    static let dsPrimary      = Color(hex: "#3B9EF5")
    static let dsPrimaryDark  = Color(hex: "#1A7DD9")
    static let dsPrimaryTint  = Color(hex: "#EAF4FF")
    static let dsText         = Color(hex: "#0D1117")
    static let dsSecondary    = Color(hex: "#6E7A8A")
    static let dsSuccess      = Color(hex: "#22C55E")
    static let dsDestructive  = Color(hex: "#EF4444")
}

extension Color {
    init(hex: String) {
        let hex = hex.trimmingCharacters(in: CharacterSet.alphanumerics.inverted)
        var int: UInt64 = 0
        Scanner(string: hex).scanHexInt64(&int)
        let r = Double((int >> 16) & 0xFF) / 255
        let g = Double((int >> 8) & 0xFF) / 255
        let b = Double(int & 0xFF) / 255
        self.init(red: r, green: g, blue: b)
    }
}

// MARK: - Typography

struct DSFont {
    static func display(_ size: CGFloat, weight: Font.Weight = .semibold) -> Font {
        .system(size: size, weight: weight, design: .rounded)
    }
    static func body(_ size: CGFloat = 14, weight: Font.Weight = .regular) -> Font {
        .system(size: size, weight: weight, design: .default)
    }
    static func mono(_ size: CGFloat = 18) -> Font {
        .system(size: size, weight: .medium, design: .monospaced)
    }
    static func caption(_ size: CGFloat = 12) -> Font {
        .system(size: size, weight: .regular, design: .default)
    }
}

// MARK: - Spacing

struct DSSpacing {
    static let xs: CGFloat  = 4
    static let sm: CGFloat  = 8
    static let md: CGFloat  = 16
    static let lg: CGFloat  = 24
    static let xl: CGFloat  = 32
    static let xxl: CGFloat = 48
}

// MARK: - Components

struct DSTextField: View {
    let placeholder: String
    @Binding var text: String
    var icon: String? = nil

    var body: some View {
        HStack(spacing: DSSpacing.sm) {
            if let icon {
                Image(systemName: icon)
                    .font(DSFont.body(14))
                    .foregroundColor(.dsSecondary)
                    .frame(width: 16)
            }
            TextField(placeholder, text: $text)
                .font(DSFont.body())
                .foregroundColor(.dsText)
                .textFieldStyle(.plain)
        }
        .padding(.horizontal, DSSpacing.md)
        .padding(.vertical, 11)
        .background(Color.dsBackground)
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(Color.dsBorder, lineWidth: 1)
        )
    }
}

struct DSButton: View {
    let title: String
    var loading: Bool = false
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: DSSpacing.sm) {
                if loading {
                    ProgressView()
                        .progressViewStyle(.circular)
                        .scaleEffect(0.7)
                        .tint(.white)
                }
                Text(title)
                    .font(DSFont.body(14, weight: .semibold))
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 11)
            .background(loading ? Color.dsPrimary.opacity(0.7) : Color.dsPrimary)
            .foregroundColor(.white)
            .cornerRadius(8)
        }
        .buttonStyle(.plain)
        .disabled(loading)
        .animation(.easeInOut(duration: 0.15), value: loading)
    }
}

struct DSStatusDot: View {
    enum Status { case connected, disconnected, connecting }
    let status: Status

    private var color: Color {
        switch status {
        case .connected:   return .dsSuccess
        case .disconnected: return .dsSecondary
        case .connecting:  return .dsPrimary
        }
    }
    private var label: String {
        switch status {
        case .connected:   return "Bağlı"
        case .disconnected: return "Bağlı değil"
        case .connecting:  return "Bağlanıyor"
        }
    }

    var body: some View {
        HStack(spacing: 6) {
            Circle()
                .fill(color)
                .frame(width: 7, height: 7)
                .overlay(
                    Circle().fill(color.opacity(0.3)).frame(width: 12, height: 12)
                        .opacity(status == .connected ? 1 : 0)
                )
            Text(label)
                .font(DSFont.caption())
                .foregroundColor(.dsSecondary)
        }
    }
}

struct DSDivider: View {
    var body: some View {
        Rectangle()
            .fill(Color.dsBorder)
            .frame(height: 1)
    }
}

// MARK: - Logo

struct DiskWaveLogo: View {
    var size: CGFloat = 36

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: size * 0.22)
                .fill(
                    LinearGradient(
                        colors: [Color.dsPrimary, Color.dsPrimaryDark],
                        startPoint: .topLeading,
                        endPoint: .bottomTrailing
                    )
                )
                .frame(width: size, height: size)

            Image(systemName: "externaldrive.fill")
                .font(.system(size: size * 0.45, weight: .medium))
                .foregroundColor(.white)

            // Signal arc
            Image(systemName: "wifi")
                .font(.system(size: size * 0.22, weight: .semibold))
                .foregroundColor(.white.opacity(0.9))
                .offset(x: size * 0.22, y: -(size * 0.22))
        }
    }
}