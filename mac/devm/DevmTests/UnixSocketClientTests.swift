import XCTest
@testable import devm

final class UnixSocketClientTests: XCTestCase {
    func testRequestAgainstLocalListener() throws {
        // Set up a tiny in-process Unix socket server that echoes a fixed
        // HTTP/1.1 response. UnixSocketClient dials it, parses, returns.
        let listener = try TestUnixListener(path: newSocketPath(), behavior: .echo(body: #"{"ok":true}"#))

        let got = try run(listener: listener)

        XCTAssertEqual(got.status, 200)
        XCTAssertEqual(got.headers["content-type"], "application/json")
        XCTAssertEqual(String(data: got.body, encoding: .utf8), #"{"ok":true}"#)
    }

    func testChunkedEncodingRoundTrip() throws {
        let listener = try TestUnixListener(path: newSocketPath(), behavior: .chunked(chunks: ["{\"a\":", "1,\"b\":2}"]))

        let got = try run(listener: listener)

        XCTAssertEqual(got.status, 200)
        XCTAssertEqual(String(data: got.body, encoding: .utf8), #"{"a":1,"b":2}"#)
    }

    func testChunkedEncodingHexBoundary() throws {
        // 0x1A (26) exercises a chunk-size line with a hex letter, and the
        // terminating "0\r\n\r\n" chunk closes the stream.
        let firstChunk = String(repeating: "x", count: 0x1A)
        let secondChunk = "tail"
        let listener = try TestUnixListener(path: newSocketPath(), behavior: .chunked(chunks: [firstChunk, secondChunk]))

        let got = try run(listener: listener)

        XCTAssertEqual(String(data: got.body, encoding: .utf8), firstChunk + secondChunk)
    }

    func testHeaderCaseInsensitivity() throws {
        let mixedCase = try TestUnixListener(path: newSocketPath(), behavior: .echo(body: "{}", headers: ["Content-Type": "application/json"]))
        let gotMixed = try run(listener: mixedCase)
        XCTAssertEqual(gotMixed.headers["content-type"], "application/json")

        let lowerCase = try TestUnixListener(path: newSocketPath(), behavior: .echo(body: "{}", headers: ["content-type": "application/json"]))
        let gotLower = try run(listener: lowerCase)
        XCTAssertEqual(gotLower.headers["content-type"], "application/json")
    }

    func testTruncatedResponseSurfacesTypedError() throws {
        // The listener writes an incomplete header block (no terminating
        // blank line) and closes the connection, simulating the daemon
        // dying mid-response. This must surface as a typed UnixSocketError,
        // never an opaque NSError and never a silently-accepted result.
        let listener = try TestUnixListener(path: newSocketPath(), behavior: .truncatedHead)

        let result = runResult(listener: listener)

        guard case .failure(let error) = result else {
            XCTFail("expected failure, got \(result)")
            return
        }
        guard let socketError = error as? UnixSocketError else {
            XCTFail("expected UnixSocketError, got opaque error: \(error)")
            return
        }
        switch socketError {
        case .malformedResponse, .readFailed:
            break
        default:
            XCTFail("expected .malformedResponse or .readFailed, got \(socketError)")
        }
    }

    func testTimeoutFires() throws {
        // The listener accepts the connection and reads the request, then
        // sleeps well past the client's timeout without ever writing a
        // response or closing the socket, simulating a hung daemon.
        let listener = try TestUnixListener(path: newSocketPath(), behavior: .hang(seconds: 2.0))

        let exp = expectation(description: "request completes")
        var got: Result<(status: Int, headers: [String: String], body: Data), Error>?
        UnixSocketClient.request(method: "GET", socketPath: listener.path, path: "/vm/status/all", body: nil, timeout: 0.3) { result in
            got = result
            exp.fulfill()
        }
        wait(for: [exp], timeout: 1.0)

        guard case .failure(let error) = got else {
            XCTFail("expected failure, got \(String(describing: got))")
            return
        }
        guard case UnixSocketError.timeout = error else {
            XCTFail("expected .timeout, got \(error)")
            return
        }
    }

    // MARK: - Helpers

    private func newSocketPath() -> String {
        URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("uds-\(UUID().uuidString).sock").path
    }

    private func run(listener: TestUnixListener) throws -> (status: Int, headers: [String: String], body: Data) {
        switch runResult(listener: listener) {
        case .success(let value): return value
        case .failure(let error): throw error
        }
    }

    private func runResult(listener: TestUnixListener) -> Result<(status: Int, headers: [String: String], body: Data), Error> {
        let exp = expectation(description: "request completes")
        var got: Result<(status: Int, headers: [String: String], body: Data), Error>!
        UnixSocketClient.request(method: "GET", socketPath: listener.path, path: "/vm/status/all", body: nil) { result in
            got = result
            exp.fulfill()
        }
        wait(for: [exp], timeout: 2.0)
        return got
    }
}

/// A tiny in-process Unix-domain socket server used only to drive
/// UnixSocketClient through real socket I/O.
final class TestUnixListener {
    enum Behavior {
        case echo(body: String, headers: [String: String] = ["Content-Type": "application/json"])
        case chunked(chunks: [String])
        case truncatedHead
        case hang(seconds: Double)
    }

    let path: String
    private let sock: Int32

    init(path: String, behavior: Behavior) throws {
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
            _ = read(client, &buf, buf.count)

            switch behavior {
            case .echo(let body, let headers):
                let response = TestUnixListener.buildResponse(headers: headers, rawBody: body)
                _ = response.withCString { write(client, $0, strlen($0)) }
            case .chunked(let chunks):
                var headers = ["Content-Type": "application/json"]
                headers["Transfer-Encoding"] = "chunked"
                var payload = TestUnixListener.buildHead(headers: headers)
                for chunk in chunks {
                    payload += String(chunk.utf8.count, radix: 16) + "\r\n" + chunk + "\r\n"
                }
                payload += "0\r\n\r\n"
                _ = payload.withCString { write(client, $0, strlen($0)) }
            case .truncatedHead:
                let partial = "HTTP/1.1 200 OK\r\nContent-Type: application"
                _ = partial.withCString { write(client, $0, strlen($0)) }
            case .hang(let seconds):
                Thread.sleep(forTimeInterval: seconds)
            }
        }
    }

    private static func buildHead(headers: [String: String]) -> String {
        var head = "HTTP/1.1 200 OK\r\n"
        for (key, value) in headers {
            head += "\(key): \(value)\r\n"
        }
        head += "\r\n"
        return head
    }

    private static func buildResponse(headers: [String: String], rawBody: String) -> String {
        var allHeaders = headers
        allHeaders["Content-Length"] = "\(rawBody.utf8.count)"
        return buildHead(headers: allHeaders) + rawBody
    }
}
