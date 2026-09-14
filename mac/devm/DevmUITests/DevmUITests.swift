import XCTest

final class DevmUITests: XCTestCase {
    func testMenuBarItemExists() {
        let app = XCUIApplication()
        app.launch()
        let statusItem = app.statusItems.firstMatch
        XCTAssertTrue(statusItem.waitForExistence(timeout: 5))
    }

    func testMenuShowsOpenAndQuitEntries() {
        let app = XCUIApplication()
        app.launch()
        let statusItem = app.statusItems.firstMatch
        XCTAssertTrue(statusItem.waitForExistence(timeout: 5))
        statusItem.click()
        XCTAssertTrue(app.menuItems["Open devm"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.menuItems["Quit"].waitForExistence(timeout: 5))
        app.typeKey(.escape, modifierFlags: [])
    }

    func testClickingOpenDevmOpensWindow() {
        let app = XCUIApplication()
        app.launch()
        let statusItem = app.statusItems.firstMatch
        XCTAssertTrue(statusItem.waitForExistence(timeout: 5))
        statusItem.click()
        let openItem = app.menuItems["Open devm"]
        XCTAssertTrue(openItem.waitForExistence(timeout: 5))
        openItem.click()
        let window = app.windows["devm"]
        XCTAssertTrue(window.waitForExistence(timeout: 5))
    }

    func testClickingQuitTerminatesApp() {
        let app = XCUIApplication()
        app.launch()
        let statusItem = app.statusItems.firstMatch
        XCTAssertTrue(statusItem.waitForExistence(timeout: 5))
        statusItem.click()
        let quitItem = app.menuItems["Quit"]
        XCTAssertTrue(quitItem.waitForExistence(timeout: 5))
        quitItem.click()
        XCTAssertTrue(app.wait(for: .notRunning, timeout: 5))
    }
}
