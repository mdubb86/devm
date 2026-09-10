import Foundation

enum UnixSocketClient {
    struct Response {
        let status: Int
        let headers: [String: String]
        let body: Data
    }

    static func request(
        method: String,
        socketPath: String,
        path: String,
        body: Data?,
        completion: @escaping (Result<(status: Int, headers: [String: String], body: Data), Error>) -> Void
    ) {
        DispatchQueue.global().async {
            let result = Result(catching: { try dialAndExchange(method: method, socketPath: socketPath, path: path, body: body) })
            DispatchQueue.main.async { completion(result.map { ($0.status, $0.headers, $0.body) }) }
        }
    }

    private static func dialAndExchange(method: String, socketPath: String, path: String, body: Data?) throws -> Response {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw error("socket: \(String(cString: strerror(errno)))") }
        defer { close(fd) }

        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        _ = socketPath.withCString { src in
            withUnsafeMutablePointer(to: &addr.sun_path) { dst in
                dst.withMemoryRebound(to: CChar.self, capacity: 104) { p in
                    _ = strncpy(p, src, 104)
                }
            }
        }
        let len = socklen_t(MemoryLayout<sockaddr_un>.size)
        let connectResult = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(fd, $0, len) }
        }
        guard connectResult == 0 else { throw error("connect \(socketPath): \(String(cString: strerror(errno)))") }

        // Compose HTTP/1.1 request.
        var request = "\(method) \(path) HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n"
        if let body = body {
            request += "Content-Length: \(body.count)\r\n\r\n"
        } else {
            request += "\r\n"
        }
        let head = Data(request.utf8)
        try writeAll(fd: fd, data: head)
        if let body = body { try writeAll(fd: fd, data: body) }

        // Read until EOF.
        var raw = Data()
        var buf = [UInt8](repeating: 0, count: 4096)
        while true {
            let n = read(fd, &buf, buf.count)
            if n <= 0 { break }
            raw.append(buf, count: n)
        }
        return try parseHTTP(raw: raw)
    }

    private static func writeAll(fd: Int32, data: Data) throws {
        var remaining = data
        while !remaining.isEmpty {
            let n = remaining.withUnsafeBytes { ptr in
                Darwin.write(fd, ptr.baseAddress, remaining.count)
            }
            if n <= 0 { throw error("write: \(String(cString: strerror(errno)))") }
            remaining.removeFirst(n)
        }
    }

    private static func parseHTTP(raw: Data) throws -> Response {
        guard let sep = raw.range(of: Data("\r\n\r\n".utf8)) else { throw error("no header terminator") }
        let head = String(data: raw.subdata(in: 0..<sep.lowerBound), encoding: .utf8) ?? ""
        let body = raw.subdata(in: sep.upperBound..<raw.count)

        var lines = head.components(separatedBy: "\r\n")
        guard !lines.isEmpty else { throw error("empty response") }
        let statusLine = lines.removeFirst()
        let parts = statusLine.split(separator: " ", maxSplits: 2)
        guard parts.count >= 2, let status = Int(parts[1]) else { throw error("bad status: \(statusLine)") }

        var headers: [String: String] = [:]
        for line in lines {
            if let idx = line.firstIndex(of: ":") {
                let k = String(line[..<idx]).trimmingCharacters(in: .whitespaces)
                let v = String(line[line.index(after: idx)...]).trimmingCharacters(in: .whitespaces)
                headers[k] = v
            }
        }
        return Response(status: status, headers: headers, body: body)
    }

    private static func error(_ msg: String) -> Error {
        NSError(domain: "UnixSocketClient", code: 1, userInfo: [NSLocalizedDescriptionKey: msg])
    }
}
