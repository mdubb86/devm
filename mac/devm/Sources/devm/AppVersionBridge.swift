import WebKit

enum AppVersionBridge {
    static func userScript() -> WKUserScript {
        let source = """
        window.__DEVM_APP_FINGERPRINT__ = \(jsStringLiteral(BuildInfo.fingerprint));
        window.__DEVM_APP_VERSION__ = \(jsStringLiteral(BuildInfo.version));
        """
        return WKUserScript(source: source, injectionTime: .atDocumentStart, forMainFrameOnly: true)
    }

    static func install(on config: WKWebViewConfiguration) {
        config.userContentController.addUserScript(userScript())
    }

    /// Renders a Swift string as a JSON-quoted string literal for splicing
    /// into injected JS source. JSON string syntax is a subset of JS string
    /// literal syntax, so the quoted form parses identically in both — this
    /// guards against BuildInfo.version (sourced from `git describe`, which
    /// a malicious tag could shape) containing a quote, backslash, or
    /// newline that would otherwise break out of the surrounding JS string
    /// literal and inject arbitrary script.
    static func jsStringLiteral(_ s: String) -> String {
        guard let data = try? JSONEncoder().encode(s),
              let str = String(data: data, encoding: .utf8) else {
            return "\"\""
        }
        return str
    }
}
