import XCTest
@testable import devm

final class BackgroundPollerTests: XCTestCase {
    func testDetectsAnyProjectDiverged() throws {
        let listener = try TestUnixListener(
            path: newSocketPath(),
            behavior: .echo(body: #"[{"name":"a","approve_state":{"diverged":false}},{"name":"b","approve_state":{"diverged":true}}]"#)
        )

        XCTAssertEqual(try poll(listener: listener), true)
    }

    func testNoDivergedProjectsReportsFalse() throws {
        let listener = try TestUnixListener(
            path: newSocketPath(),
            behavior: .echo(body: #"[{"name":"a","approve_state":{"diverged":false}}]"#)
        )

        XCTAssertEqual(try poll(listener: listener), false)
    }

    func testMissingApproveStateTreatedAsNotDiverged() throws {
        let listener = try TestUnixListener(path: newSocketPath(), behavior: .echo(body: #"[{"name":"a"}]"#))

        XCTAssertEqual(try poll(listener: listener), false)
    }

    // MARK: - Helpers

    private func newSocketPath() -> String {
        URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("uds-\(UUID().uuidString).sock").path
    }

    private func poll(listener: TestUnixListener) throws -> Bool {
        let exp = expectation(description: "update received")
        var got: Bool?
        let poller = BackgroundPoller(socketPath: listener.path) { anyDiverged in
            got = anyDiverged
            exp.fulfill()
        }
        poller.start()
        defer { poller.stop() }
        wait(for: [exp], timeout: 2.0)
        return try XCTUnwrap(got)
    }
}
