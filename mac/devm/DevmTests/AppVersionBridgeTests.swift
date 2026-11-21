import XCTest
import WebKit
@testable import devm

final class AppVersionBridgeTests: XCTestCase {

    func testUserScriptInjectsFingerprintAndVersion() {
        let script = AppVersionBridge.userScript()

        XCTAssertTrue(script.source.contains("window.__DEVM_APP_FINGERPRINT__ = \"\(BuildInfo.fingerprint)\";"))
        XCTAssertTrue(script.source.contains("window.__DEVM_APP_VERSION__ = \"\(BuildInfo.version)\";"))
    }

    func testJSStringLiteralEscapesQuotesBackslashesAndControlChars() {
        // BuildInfo.version is sourced from `git describe --tags --always`,
        // which a malicious tag could shape to contain quotes, backslashes,
        // or JS-terminator characters. jsStringLiteral must render a value
        // that is safe to splice directly into JS source.
        let input = "foo\"bar\\baz\n"
        let literal = AppVersionBridge.jsStringLiteral(input)

        XCTAssertEqual(literal, #""foo\"bar\\baz\n""#)

        // Round-trip through JSONDecoder to confirm it's valid JSON (and
        // therefore valid-as-JS-string-literal) syntax that decodes back to
        // the original value.
        let decoded = try? JSONDecoder().decode(String.self, from: Data(literal.utf8))
        XCTAssertEqual(decoded, input)
    }

    func testJSStringLiteralHandlesPlainStrings() {
        XCTAssertEqual(AppVersionBridge.jsStringLiteral("v1.2.3"), "\"v1.2.3\"")
        XCTAssertEqual(AppVersionBridge.jsStringLiteral(""), "\"\"")
    }

    func testUserScriptRunsAtDocumentStart() {
        let script = AppVersionBridge.userScript()

        XCTAssertEqual(script.injectionTime, .atDocumentStart)
        XCTAssertTrue(script.isForMainFrameOnly)
    }

    func testInstallAttachesScriptToConfiguration() {
        let config = WKWebViewConfiguration()
        AppVersionBridge.install(on: config)

        XCTAssertEqual(config.userContentController.userScripts.count, 1)
        XCTAssertEqual(config.userContentController.userScripts.first?.injectionTime, .atDocumentStart)
    }
}
