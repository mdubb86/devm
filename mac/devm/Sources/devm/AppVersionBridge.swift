import WebKit

enum AppVersionBridge {
    static func userScript() -> WKUserScript {
        let source = """
        window.__DEVM_APP_FINGERPRINT__ = "\(BuildInfo.fingerprint)";
        window.__DEVM_APP_VERSION__ = "\(BuildInfo.version)";
        """
        return WKUserScript(source: source, injectionTime: .atDocumentStart, forMainFrameOnly: true)
    }

    static func install(on config: WKWebViewConfiguration) {
        config.userContentController.addUserScript(userScript())
    }
}
