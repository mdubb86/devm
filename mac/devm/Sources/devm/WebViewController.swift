import AppKit
import WebKit

final class WebViewController: NSViewController {
    var webView: WKWebView!

    override func loadView() {
        let config = WKWebViewConfiguration()
        let handler = DevmAPIURLSchemeHandler(socketPath: AppIdentity.current.socketPath)
        config.setURLSchemeHandler(handler, forURLScheme: "devm-api")

        let wv = WKWebView(frame: .zero, configuration: config)
        if #available(macOS 13.3, *) {
            wv.isInspectable = true
        }
        self.webView = wv
        self.view = wv

        if let url = Bundle.main.url(forResource: "gui", withExtension: "html") {
            wv.loadFileURL(url, allowingReadAccessTo: url.deletingLastPathComponent())
        }
    }
}
