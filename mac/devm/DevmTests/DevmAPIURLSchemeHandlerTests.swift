import XCTest
import WebKit
@testable import devm

final class DevmAPIURLSchemeHandlerTests: XCTestCase {

    // MARK: - Success

    func testForwardsRequestToSocketAndReturnsResponse() throws {
        let listener = try TestUnixListener(path: newSocketPath(), behavior: .echo(body: #"[{"name":"proj"}]"#))

        let handler = DevmAPIURLSchemeHandler(socketPath: listener.path)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm/status/all")!)

        let exp = expectation(description: "task finishes")
        task.onFinish = { exp.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [exp], timeout: 2.0)

        XCTAssertNil(task.receivedError)
        XCTAssertEqual((task.receivedResponse as? HTTPURLResponse)?.statusCode, 200)
        XCTAssertEqual(String(data: task.receivedData, encoding: .utf8), #"[{"name":"proj"}]"#)
    }

    // MARK: - URL translation

    func testTranslatesHostAndPathToDaemonPath() throws {
        let listener = try CapturingUnixListener(path: newSocketPath())

        let handler = DevmAPIURLSchemeHandler(socketPath: listener.path)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm/status/all")!)

        let exp = expectation(description: "task finishes")
        task.onFinish = { exp.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [exp], timeout: 2.0)

        XCTAssertEqual(listener.capturedRequestLine, "GET /status/all HTTP/1.1")
    }

    func testPreservesQueryString() throws {
        let listener = try CapturingUnixListener(path: newSocketPath())

        let handler = DevmAPIURLSchemeHandler(socketPath: listener.path)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm/status/all?project=p")!)

        let exp = expectation(description: "task finishes")
        task.onFinish = { exp.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [exp], timeout: 2.0)

        XCTAssertEqual(listener.capturedRequestLine, "GET /status/all?project=p HTTP/1.1")
    }

    func testDropsFragment() throws {
        let listener = try CapturingUnixListener(path: newSocketPath())

        let handler = DevmAPIURLSchemeHandler(socketPath: listener.path)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm/status/all?project=p#section")!)

        let exp = expectation(description: "task finishes")
        task.onFinish = { exp.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [exp], timeout: 2.0)

        XCTAssertEqual(listener.capturedRequestLine, "GET /status/all?project=p HTTP/1.1")
    }

    func testDaemonPathHelperDirectly() {
        XCTAssertEqual(
            DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api://vm/status/all")!),
            "/status/all"
        )
        XCTAssertEqual(
            DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api://vm/status/all?project=p")!),
            "/status/all?project=p"
        )
        XCTAssertEqual(
            DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api://vm/status/all?project=p#frag")!),
            "/status/all?project=p"
        )
        XCTAssertEqual(
            DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api://vm/status/foo%20bar")!),
            "/status/foo%20bar"
        )
        XCTAssertEqual(
            DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api://vm/status%2Fall")!),
            "/status%2Fall"
        )
        XCTAssertEqual(
            DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api://vm/status/all/")!),
            "/status/all/"
        )
        XCTAssertEqual(
            DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api://vm/status/all?project=a%2Fb")!),
            "/status/all?project=a%2Fb"
        )
        XCTAssertEqual(
            DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api:///vm/status/all")!),
            "/vm/status/all"
        )
        XCTAssertEqual(
            DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api://vm/version")!),
            "/version"
        )
        XCTAssertNil(DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api://vm")!))
    }

    func testDaemonPathDoesNotIncludeHostSegment() {
        // The bug this guards: daemonPath(for:) must forward only the URL's
        // path component to the daemon. The daemon's routes (/status/all,
        // /version, etc.) have no /vm prefix — "vm" is a syntactic
        // placeholder host, not a daemon route segment.
        XCTAssertEqual(
            DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api://vm/status/all")!),
            "/status/all"
        )
        XCTAssertNotEqual(
            DevmAPIURLSchemeHandler.daemonPath(for: URL(string: "devm-api://vm/status/all")!),
            "/vm/status/all"
        )
    }

    // MARK: - Percent-encoding and trailing-slash fidelity on the wire

    func testPercentEncodedSpacePreservedOnWire() throws {
        let listener = try CapturingUnixListener(path: newSocketPath())

        let handler = DevmAPIURLSchemeHandler(socketPath: listener.path)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm/status/foo%20bar")!)

        let exp = expectation(description: "task finishes")
        task.onFinish = { exp.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [exp], timeout: 2.0)

        XCTAssertEqual(listener.capturedRequestLine, "GET /status/foo%20bar HTTP/1.1")
    }

    func testPercentEncodedSlashPreservedOnWire() throws {
        let listener = try CapturingUnixListener(path: newSocketPath())

        let handler = DevmAPIURLSchemeHandler(socketPath: listener.path)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm/status%2Fall")!)

        let exp = expectation(description: "task finishes")
        task.onFinish = { exp.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [exp], timeout: 2.0)

        XCTAssertEqual(listener.capturedRequestLine, "GET /status%2Fall HTTP/1.1")
    }

    func testTrailingSlashPreservedOnWire() throws {
        let listener = try CapturingUnixListener(path: newSocketPath())

        let handler = DevmAPIURLSchemeHandler(socketPath: listener.path)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm/status/all/")!)

        let exp = expectation(description: "task finishes")
        task.onFinish = { exp.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [exp], timeout: 2.0)

        XCTAssertEqual(listener.capturedRequestLine, "GET /status/all/ HTTP/1.1")
    }

    func testPercentEncodedQueryPreservedOnWire() throws {
        let listener = try CapturingUnixListener(path: newSocketPath())

        let handler = DevmAPIURLSchemeHandler(socketPath: listener.path)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm/status/all?project=a%2Fb")!)

        let exp = expectation(description: "task finishes")
        task.onFinish = { exp.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [exp], timeout: 2.0)

        XCTAssertEqual(listener.capturedRequestLine, "GET /status/all?project=a%2Fb HTTP/1.1")
    }

    func testEmptyHostStillWorksOnWire() throws {
        let listener = try CapturingUnixListener(path: newSocketPath())

        let handler = DevmAPIURLSchemeHandler(socketPath: listener.path)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api:///vm/status/all")!)

        let exp = expectation(description: "task finishes")
        task.onFinish = { exp.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [exp], timeout: 2.0)

        XCTAssertEqual(listener.capturedRequestLine, "GET /vm/status/all HTTP/1.1")
    }

    func testMissingPathFailsWithUnparsableRequestURL() throws {
        // "devm-api://vm" has no path beyond the placeholder host, so
        // there is no daemon route to forward to — the handler must fail
        // the task rather than send an empty or host-only request.
        let handler = DevmAPIURLSchemeHandler(socketPath: newSocketPath())
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm")!)

        let exp = expectation(description: "task finishes")
        task.onFinish = { exp.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [exp], timeout: 2.0)

        guard let error = task.receivedError as? DevmAPIURLSchemeHandlerError else {
            XCTFail("expected DevmAPIURLSchemeHandlerError, got \(String(describing: task.receivedError))")
            return
        }
        switch error {
        case .unparsableRequestURL:
            break
        default:
            XCTFail("expected .unparsableRequestURL, got \(error)")
        }
    }

    // MARK: - Failure

    func testForwardsSocketErrorAsFailure() throws {
        // No listener bound at this path — the socket connect fails, which
        // must surface via didFailWithError rather than being swallowed.
        let missingSocketPath = newSocketPath()

        let handler = DevmAPIURLSchemeHandler(socketPath: missingSocketPath)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm/status/all")!)

        let exp = expectation(description: "task finishes")
        task.onFinish = { exp.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [exp], timeout: 2.0)

        XCTAssertNotNil(task.receivedError)
        XCTAssertTrue(task.receivedData.isEmpty)
        XCTAssertNil(task.receivedResponse)
        guard let socketError = task.receivedError as? UnixSocketError else {
            XCTFail("expected UnixSocketError, got \(String(describing: task.receivedError))")
            return
        }
        switch socketError {
        case .connectFailed:
            break
        default:
            XCTFail("expected .connectFailed, got \(socketError)")
        }
    }

    // MARK: - Stop safety

    func testNoDeliveryAfterStop() throws {
        // The listener hangs well past our wait window, so the completion
        // would only ever fire after we call stop() — if the handler does
        // not guard against post-stop delivery, this test would hang or a
        // later run would crash the (real) WKWebView on a stopped task.
        let listener = try TestUnixListener(path: newSocketPath(), behavior: .hang(seconds: 1.5))

        let handler = DevmAPIURLSchemeHandler(socketPath: listener.path)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm/status/all")!)

        let neverFinishes = expectation(description: "task must not finish after stop")
        neverFinishes.isInverted = true
        task.onFinish = { neverFinishes.fulfill() }

        handler.webView(webView, start: task)
        handler.webView(webView, stop: task)

        wait(for: [neverFinishes], timeout: 2.5)

        XCTAssertNil(task.receivedError)
        XCTAssertNil(task.receivedResponse)
        XCTAssertTrue(task.receivedData.isEmpty)
    }

    func testStoppedTaskEntryClearedAfterCompletionAllowsReuseOfSameObjectIdentifier() throws {
        // Regression test: stoppedTasks must not grow unbounded across the
        // resident menu-bar app's process lifetime. If a taskID isn't
        // removed once its completion has run, a later WKURLSchemeTask that
        // reuses the same ObjectIdentifier (a real risk once the first task
        // is deallocated) would be wrongly treated as already-stopped and
        // have its response silently dropped. We reuse the same
        // MockURLSchemeTask instance (guaranteeing the same
        // ObjectIdentifier) across two request cycles to prove the first
        // cycle's stop entry doesn't poison the second.
        // TestUnixListener accepts exactly one connection then closes and
        // unlinks its socket path, so each request cycle below binds its
        // own listener to the same path (free again once the prior
        // listener's server-side thread finishes, which happens
        // independently of whether the client stopped).
        let socketPath = newSocketPath()
        let handler = DevmAPIURLSchemeHandler(socketPath: socketPath)
        let webView = WKWebView()
        let task = MockURLSchemeTask(url: URL(string: "devm-api://vm/status/all")!)

        // First cycle: stop before the socket completion can run, so the
        // completion's isStopped check drops the response and the handler
        // is left holding a stoppedTasks entry for this ObjectIdentifier.
        let firstListener = try TestUnixListener(path: socketPath, behavior: .echo(body: #"{"ok":true}"#))
        let firstNeverFinishes = expectation(description: "stopped task must not finish")
        firstNeverFinishes.isInverted = true
        task.onFinish = { firstNeverFinishes.fulfill() }
        handler.webView(webView, start: task)
        handler.webView(webView, stop: task)
        wait(for: [firstNeverFinishes], timeout: 1.0)
        _ = firstListener

        // Let the in-flight completion run to (a) confirm it drops the
        // response and (b) clear the stoppedTasks entry.
        let settle = expectation(description: "settle")
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { settle.fulfill() }
        wait(for: [settle], timeout: 2.0)

        // Second cycle: same object identity, never stopped, fresh listener
        // on the now-free path. If the first cycle's entry wasn't cleared,
        // this would be wrongly dropped too.
        let secondListener = try TestUnixListener(path: socketPath, behavior: .echo(body: #"{"ok":true}"#))
        let secondFinishes = expectation(description: "reused-identity task delivers")
        task.onFinish = { secondFinishes.fulfill() }
        handler.webView(webView, start: task)
        wait(for: [secondFinishes], timeout: 2.0)
        _ = secondListener

        XCTAssertNil(task.receivedError)
        XCTAssertEqual((task.receivedResponse as? HTTPURLResponse)?.statusCode, 200)
        XCTAssertEqual(String(data: task.receivedData, encoding: .utf8), #"{"ok":true}"#)
    }

    // MARK: - Helpers

    private func newSocketPath() -> String {
        NSTemporaryDirectory() + "handler-\(UUID().uuidString).sock"
    }
}

// Minimal WKURLSchemeTask stand-in — WKURLSchemeTask is a protocol,
// so we implement just the methods our handler calls.
final class MockURLSchemeTask: NSObject, WKURLSchemeTask {
    var request: URLRequest
    var receivedResponse: URLResponse?
    var receivedData = Data()
    var receivedError: Error?
    var onFinish: (() -> Void)?

    init(url: URL) {
        var r = URLRequest(url: url)
        r.httpMethod = "GET"
        self.request = r
    }
    func didReceive(_ response: URLResponse) { receivedResponse = response }
    func didReceive(_ data: Data) { receivedData.append(data) }
    func didFinish() { onFinish?() }
    func didFailWithError(_ error: Error) { receivedError = error; onFinish?() }
}

/// A tiny in-process Unix-domain socket server that captures the request
/// line it received before writing a fixed 200 response, used to assert
/// on the exact path DevmAPIURLSchemeHandler sent to the daemon.
final class CapturingUnixListener {
    let path: String
    private(set) var capturedRequestLine: String?
    private let sock: Int32

    init(path: String) throws {
        self.path = path
        sock = socket(AF_UNIX, SOCK_STREAM, 0)
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        _ = path.withCString { src in
            withUnsafeMutablePointer(to: &addr.sun_path) { dst in
                dst.withMemoryRebound(to: CChar.self, capacity: 104) { p in
                    _ = strncpy(p, src, 104)
                }
            }
        }
        let len = socklen_t(MemoryLayout<sockaddr_un>.size)
        let bindResult = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.bind(sock, $0, len) }
        }
        guard bindResult == 0 else { throw NSError(domain: "test", code: Int(errno)) }
        guard listen(sock, 1) == 0 else { throw NSError(domain: "test", code: Int(errno)) }

        DispatchQueue.global().async { [self] in
            let client = accept(sock, nil, nil)
            guard client >= 0 else { return }
            var noSigPipe: Int32 = 1
            _ = setsockopt(client, SOL_SOCKET, SO_NOSIGPIPE, &noSigPipe, socklen_t(MemoryLayout<Int32>.size))
            defer { close(client); close(sock); unlink(path) }

            var buf = [UInt8](repeating: 0, count: 4096)
            let n = read(client, &buf, buf.count)
            if n > 0, let received = String(bytes: buf[0..<n], encoding: .utf8) {
                capturedRequestLine = received.components(separatedBy: "\r\n").first
            }

            let body = "{}"
            let response = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: \(body.utf8.count)\r\n\r\n\(body)"
            _ = response.withCString { write(client, $0, strlen($0)) }
        }
    }
}
