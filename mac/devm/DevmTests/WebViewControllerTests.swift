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
}
