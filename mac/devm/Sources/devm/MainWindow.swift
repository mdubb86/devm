import AppKit

final class MainWindow {
    static let shared = MainWindow()
    private var window: NSWindow?

    func show() {
        if let existing = window {
            existing.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            return
        }
        let vc = WebViewController()
        let w = NSWindow(contentViewController: vc)
        w.setContentSize(NSSize(width: 900, height: 600))
        w.styleMask = [.titled, .closable, .miniaturizable, .resizable]
        w.title = "devm"
        w.center()
        w.isReleasedWhenClosed = false
        window = w
        w.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }
}
