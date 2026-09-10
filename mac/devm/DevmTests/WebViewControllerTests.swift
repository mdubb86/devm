import XCTest
import WebKit
@testable import devm

final class WebViewControllerTests: XCTestCase {

    func testConfiguresSchemeHandler() {
        let vc = WebViewController()
        vc.loadView()

        let handler = vc.webView.configuration.urlSchemeHandler(forURLScheme: "devm-api")
        XCTAssertNotNil(handler)
        XCTAssertTrue(handler is DevmAPIURLSchemeHandler)
    }

    func testInstallsAppVersionBridge() {
        let vc = WebViewController()
        vc.loadView()

        let scripts = vc.webView.configuration.userContentController.userScripts
        XCTAssertTrue(scripts.contains { $0.source.contains("window.__DEVM_APP_FINGERPRINT__") })
    }
}
