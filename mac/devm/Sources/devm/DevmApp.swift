import SwiftUI

@main
struct DevmApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate
    @ObservedObject private var badgeState = BadgeState.shared

    var body: some Scene {
        MenuBarExtra {
            Button("Open devm") { MainWindow.shared.show() }
            Divider()
            Button("Quit") { NSApp.terminate(nil) }
        } label: {
            Image(systemName: badgeState.anyDiverged ? "hammer.circle.fill" : "hammer.circle")
                .accessibilityIdentifier(badgeState.anyDiverged ? "devm-status-diverged" : "devm-status-ok")
        }
        .menuBarExtraStyle(.menu)
    }
}

class AppDelegate: NSObject, NSApplicationDelegate {
    private var poller: BackgroundPoller?

    func applicationDidFinishLaunching(_ notification: Notification) {
        if CommandLine.arguments.contains("--show-window") {
            MainWindow.shared.show()
        }

        // Test-only seed hook: XCUITest has no controllable data source for
        // the real daemon, so it drives the badge directly instead of
        // starting the poller.
        if ProcessInfo.processInfo.environment["DEVM_TEST_SEED_DIVERGED"] == "1" {
            BadgeState.shared.anyDiverged = true
            return
        }

        let poller = BackgroundPoller(socketPath: AppIdentity.current.socketPath) { anyDiverged in
            DispatchQueue.main.async {
                BadgeState.shared.anyDiverged = anyDiverged
            }
        }
        self.poller = poller
        poller.start()
    }
}
