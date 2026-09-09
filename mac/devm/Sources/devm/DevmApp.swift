import SwiftUI

@main
struct DevmApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    var body: some Scene {
        MenuBarExtra("devm", image: "MenuBarIcon") {
            Button("Open devm") { MainWindow.shared.show() }
            Divider()
            Button("Quit") { NSApp.terminate(nil) }
        }
        .menuBarExtraStyle(.menu)
    }
}

class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        // LSUIElement=true keeps us out of the dock; nothing to do here yet.
    }
}
