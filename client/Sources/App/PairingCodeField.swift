import SwiftUI

// Tek satır — 6 karakter, her biri kendi kutusunda
struct PairingCodeField: View {
    @Binding var code: String
    @FocusState private var focused: Bool

    private let length = 6

    var body: some View {
        ZStack {
            // Gerçek input — görünmez
            TextField("", text: Binding(
                get: { code },
                set: { new in
                    let f = new.uppercased().filter { $0.isLetter || $0.isNumber }
                    code = String(f.prefix(length))
                }
            ))
            .focused($focused)
            .opacity(0)
            .frame(width: 1, height: 1)

            // Kutular
            HStack(spacing: 6) {
                ForEach(0..<length, id: \.self) { i in
                    CodeBox(
                        char: char(at: i),
                        active: focused && i == min(code.count, length - 1),
                        filled: i < code.count
                    )
                }
            }
            .onTapGesture { focused = true }
        }
        .onAppear { focused = true }
    }

    private func char(at i: Int) -> String {
        guard i < code.count else { return "" }
        return String(code[code.index(code.startIndex, offsetBy: i)])
    }
}

private struct CodeBox: View {
    let char: String
    let active: Bool
    let filled: Bool

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 7)
                .fill(Color(.controlBackgroundColor))
                .overlay(
                    RoundedRectangle(cornerRadius: 7)
                        .stroke(
                            active ? Color(hex: "#1A9AE0") :
                            filled  ? Color(hex: "#1A9AE0").opacity(0.4) :
                                      Color(.separatorColor),
                            lineWidth: active ? 1.5 : 1
                        )
                )

            Text(char)
                .font(.system(size: 18, weight: .medium, design: .monospaced))
                .foregroundColor(Color(.labelColor))
        }
        .frame(width: 42, height: 48)
        .animation(.easeInOut(duration: 0.1), value: active)
        .animation(.easeInOut(duration: 0.1), value: filled)
    }
}