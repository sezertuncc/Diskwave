import SwiftUI

@main
struct DiskWaveApp: App {
    @ObservedObject var state = AppState.shared

    var body: some Scene {
        MenuBarExtra {
            MenuBarView()
                .environmentObject(AppState.shared)
        } label: {
            MenuBarIcon()
        }
        .menuBarExtraStyle(.window)
    }
}