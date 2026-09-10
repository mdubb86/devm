import XCTest
@testable import devm

final class UnixSocketClientTests: XCTestCase {
    func testRequestAgainstLocalListener() throws {
        // Set up a tiny in-process Unix socket server that echoes a fixed
        // HTTP/1.1 response. UnixSocketClient dials it, parses, returns.
        let tmp = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("uds-\(UUID().uuidString).sock").path
        let listener = try TestUnixListener(path: tmp)
        listener.responseBody = #"{"ok":true}"#

        let exp = expectation(description: "request completes")
        var got: (status: Int, headers: [String: String], body: Data)?
        UnixSocketClient.request(method: "GET", socketPath: tmp, path: "/vm/status/all", body: nil) { result in
            if case .success(let value) = result { got = value }
            exp.fulfill()
        }
        wait(for: [exp], timeout: 2.0)

        XCTAssertNotNil(got)
        XCTAssertEqual(got?.status, 200)
        XCTAssertEqual(got?.headers["Content-Type"], "application/json")
        XCTAssertEqual(String(data: got!.body, encoding: .utf8), #"{"ok":true}"#)
    }
}

final class TestUnixListener {
    let path: String
    var responseBody: String = ""
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
            defer { close(client); close(sock); unlink(path) }
            var buf = [UInt8](repeating: 0, count: 4096)
            _ = read(client, &buf, buf.count)
            let body = responseBody
            let response = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: \(body.utf8.count)\r\n\r\n\(body)"
            _ = response.withCString { write(client, $0, strlen($0)) }
        }
    }
}
