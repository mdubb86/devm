import Foundation

enum UnixSocketError: Error {
    case socketCreateFailed(errno: Int32)
    case connectFailed(path: String, errno: Int32)
    case writeFailed(errno: Int32)
    case readFailed(errno: Int32)
    case timeout
    case malformedResponse(String)
    case unsupportedTransferEncoding(String)
}

extension UnixSocketError: LocalizedError {
    var errorDescription: String? {
        switch self {
        case .socketCreateFailed(let errno):
            return "socket: \(String(cString: strerror(errno)))"
        case .connectFailed(let path, let errno):
            return "connect \(path): \(String(cString: strerror(errno)))"
        case .writeFailed(let errno):
            return "write: \(String(cString: strerror(errno)))"
        case .readFailed(let errno):
            return "read: \(String(cString: strerror(errno)))"
        case .timeout:
            return "timed out waiting for daemon response"
        case .malformedResponse(let detail):
            return "malformed HTTP response: \(detail)"
        case .unsupportedTransferEncoding(let encoding):
            return "unsupported Transfer-Encoding: \(encoding)"
        }
    }
}

enum UnixSocketClient {
    struct Response {
        let status: Int
        let headers: [String: String]
        let body: Data
    }

    static let defaultTimeout: TimeInterval = 10.0

    static func request(
        method: String,
        socketPath: String,
        path: String,
        body: Data?,
        timeout: TimeInterval = defaultTimeout,
        completion: @escaping (Result<(status: Int, headers: [String: String], body: Data), Error>) -> Void
    ) {
        DispatchQueue.global().async {
            let result = Result(catching: { try dialAndExchange(method: method, socketPath: socketPath, path: path, body: body, timeout: timeout) })
            DispatchQueue.main.async { completion(result.map { ($0.status, $0.headers, $0.body) }) }
        }
    }

    private static func dialAndExchange(method: String, socketPath: String, path: String, body: Data?, timeout: TimeInterval) throws -> Response {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw UnixSocketError.socketCreateFailed(errno: errno) }
        defer { close(fd) }

        var noSigPipe: Int32 = 1
        _ = setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSigPipe, socklen_t(MemoryLayout<Int32>.size))

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
        guard connectResult == 0 else { throw UnixSocketError.connectFailed(path: socketPath, errno: errno) }

        var tv = timeval()
        tv.tv_sec = Int(timeout)
        tv.tv_usec = Int32((timeout - Double(Int(timeout))) * 1_000_000)
        _ = setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        _ = setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))

        var request = "\(method) \(path) HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n"
        if let body = body {
            request += "Content-Length: \(body.count)\r\n\r\n"
        } else {
            request += "\r\n"
        }
        let head = Data(request.utf8)
        try writeAll(fd: fd, data: head)
        if let body = body { try writeAll(fd: fd, data: body) }

        var raw = Data()
        var buf = [UInt8](repeating: 0, count: 4096)
        while true {
            let n = read(fd, &buf, buf.count)
            if n > 0 {
                raw.append(buf, count: n)
                continue
            }
            if n == 0 { break }
            if errno == EAGAIN || errno == EWOULDBLOCK { throw UnixSocketError.timeout }
            throw UnixSocketError.readFailed(errno: errno)
        }
        return try parseHTTP(raw: raw)
    }

    private static func writeAll(fd: Int32, data: Data) throws {
        var remaining = data
        while !remaining.isEmpty {
            let n = remaining.withUnsafeBytes { ptr in
                Darwin.write(fd, ptr.baseAddress, remaining.count)
            }
            if n > 0 {
                remaining.removeFirst(n)
                continue
            }
            if errno == EAGAIN || errno == EWOULDBLOCK { throw UnixSocketError.timeout }
            throw UnixSocketError.writeFailed(errno: errno)
        }
    }

    private static func parseHTTP(raw: Data) throws -> Response {
        guard let sep = raw.range(of: Data("\r\n\r\n".utf8)) else { throw UnixSocketError.malformedResponse("no header terminator") }
        let head = String(data: raw.subdata(in: 0..<sep.lowerBound), encoding: .utf8) ?? ""
        var body = raw.subdata(in: sep.upperBound..<raw.count)

        var lines = head.components(separatedBy: "\r\n")
        guard !lines.isEmpty else { throw UnixSocketError.malformedResponse("empty response") }
        let statusLine = lines.removeFirst()
        let parts = statusLine.split(separator: " ", maxSplits: 2)
        guard parts.count >= 2, let status = Int(parts[1]) else { throw UnixSocketError.malformedResponse("bad status: \(statusLine)") }

        var headers: [String: String] = [:]
        for line in lines {
            if let idx = line.firstIndex(of: ":") {
                let k = String(line[..<idx]).trimmingCharacters(in: .whitespaces).lowercased()
                let v = String(line[line.index(after: idx)...]).trimmingCharacters(in: .whitespaces)
                headers[k] = v
            }
        }

        if let transferEncoding = headers["transfer-encoding"] {
            guard transferEncoding.lowercased() == "chunked" else {
                throw UnixSocketError.unsupportedTransferEncoding(transferEncoding)
            }
            body = try decodeChunked(body)
        }

        return Response(status: status, headers: headers, body: body)
    }

    private static func decodeChunked(_ raw: Data) throws -> Data {
        let bytes = [UInt8](raw)
        var result = Data()
        var offset = 0
        while true {
            guard let crlfIdx = indexOfCRLF(bytes, from: offset) else {
                throw UnixSocketError.malformedResponse("chunked: missing chunk-size line terminator")
            }
            guard let sizeLine = String(bytes: bytes[offset..<crlfIdx], encoding: .ascii) else {
                throw UnixSocketError.malformedResponse("chunked: non-ASCII chunk-size line")
            }
            let hexPart = sizeLine.split(separator: ";", maxSplits: 1)[0].trimmingCharacters(in: .whitespaces)
            guard let size = Int(hexPart, radix: 16) else {
                throw UnixSocketError.malformedResponse("chunked: bad chunk size '\(sizeLine)'")
            }
            let dataStart = crlfIdx + 2
            if size == 0 { break }
            guard dataStart + size + 2 <= bytes.count else {
                throw UnixSocketError.malformedResponse("chunked: truncated chunk data")
            }
            result.append(contentsOf: bytes[dataStart..<(dataStart + size)])
            let trailerStart = dataStart + size
            guard bytes[trailerStart] == 0x0D, bytes[trailerStart + 1] == 0x0A else {
                throw UnixSocketError.malformedResponse("chunked: missing CRLF after chunk data")
            }
            offset = trailerStart + 2
        }
        return result
    }

    private static func indexOfCRLF(_ bytes: [UInt8], from start: Int) -> Int? {
        guard start < bytes.count else { return nil }
        var i = start
        while i + 1 < bytes.count {
            if bytes[i] == 0x0D && bytes[i + 1] == 0x0A { return i }
            i += 1
        }
        return nil
    }
}
