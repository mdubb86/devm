import SwiftUI

@main
struct DevmApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    var body: some Scene {
        MenuBarExtra("devm", systemImage: "hammer.circle") {
            Button("Open devm") { MainWindow.shared.show() }
            Divider()
            Button("Quit") { NSApp.terminate(nil) }
        }
        .menuBarExtraStyle(.menu)
    }
}

class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        if CommandLine.arguments.contains("--show-window") {
            MainWindow.shared.show()
        }
    }
}
