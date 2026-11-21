import XCTest
@testable import devm

final class BuildInfoTests: XCTestCase {
    func testFingerprintIsSet() {
        XCTAssertFalse(BuildInfo.fingerprint.isEmpty)
    }

    func testVersionIsSet() {
        XCTAssertFalse(BuildInfo.version.isEmpty)
    }
}
