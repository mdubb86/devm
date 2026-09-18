import XCTest

/// Menu-bar badge coverage. NSStatusItem's accessibility surface under
/// XCUITest is minimal and version-sensitive — whether SwiftUI's
/// `.accessibilityIdentifier` on the MenuBarExtra label view actually reaches
/// the underlying NSStatusBarButton's accessibility element is not
/// guaranteed across macOS/Xcode versions. These tests are best-effort: they
/// exercise the real launch path (via `DEVM_TEST_SEED_DIVERGED`, the same
/// hook AppDelegate uses to bypass the network poller in tests) and assert
/// on the identifier DevmApp.swift attaches to the status item's label
/// image. If the identifier stops surfacing to the accessibility tree on a
/// future OS/Xcode update, treat that as a platform regression to
/// re-investigate, not a reason to delete the test.
final class BadgeTests: XCTestCase {
    func testBadgeShowsDivergedIdentifierWhenSeeded() throws {
        let app = XCUIApplication()
        app.launchEnvironment["DEVM_TEST_SEED_DIVERGED"] = "1"
        app.launch()

        let statusItem = app.statusItems.firstMatch
        XCTAssertTrue(statusItem.waitForExistence(timeout: 10.0))

        let diverged = app.statusItems["devm-status-diverged"]
        XCTAssertTrue(
            diverged.waitForExistence(timeout: 10.0),
            "expected the diverged badge identifier on the status item"
        )
    }

    func testBadgeShowsOkIdentifierWithoutSeed() throws {
        let app = XCUIApplication()
        app.launch()

        let statusItem = app.statusItems.firstMatch
        XCTAssertTrue(statusItem.waitForExistence(timeout: 10.0))

        let ok = app.statusItems["devm-status-ok"]
        XCTAssertTrue(
            ok.waitForExistence(timeout: 10.0),
            "expected the default (non-diverged) badge identifier on the status item"
        )
    }
}
