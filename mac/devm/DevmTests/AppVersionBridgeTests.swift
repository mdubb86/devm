import XCTest
import WebKit
@testable import devm

final class AppVersionBridgeTests: XCTestCase {

    func testUserScriptInjectsFingerprintAndVersion() {
        let script = AppVersionBridge.userScript()

        XCTAssertTrue(script.source.contains("window.__DEVM_APP_FINGERPRINT__ = \"\(BuildInfo.fingerprint)\";"))
        XCTAssertTrue(script.source.contains("window.__DEVM_APP_VERSION__ = \"\(BuildInfo.version)\";"))
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
