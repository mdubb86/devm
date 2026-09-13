import Foundation
import WebKit
import os.log

private let schemeLog = Logger(subsystem: "com.mdubb86.devm.mac", category: "scheme")

enum DevmAPIURLSchemeHandlerError: Error {
    case missingRequestURL
    case unparsableRequestURL(URL)
    case invalidHTTPResponse(status: Int)
}

extension DevmAPIURLSchemeHandlerError: LocalizedError {
    var errorDescription: String? {
        switch self {
        case .missingRequestURL:
            return "devm-api request had no URL"
        case .unparsableRequestURL(let url):
            return "could not parse devm-api request URL: \(url)"
        case .invalidHTTPResponse(let status):
            return "could not construct an HTTP response for status \(status)"
        }
    }
}

/// Translates `devm-api://` fetches made by the webview into requests
/// against the daemon's Unix socket.
///
/// `devm-api://vm/status/all?project=x` parses (per RFC 3986, since a
/// custom scheme followed by "//" introduces an authority component) with
/// host="vm" and path="/status/all". The daemon's routes have no `/vm`
/// prefix, so only the path (plus query) is forwarded: "/status/all?project=x".
/// The "vm" host is a placeholder required by URL syntax — RFC 3986 needs
/// something between `://` and the path — and carries no meaning to the
/// daemon.
final class DevmAPIURLSchemeHandler: NSObject, WKURLSchemeHandler {
    let socketPath: String

    private let lock = NSLock()
    private var stoppedTasks = Set<ObjectIdentifier>()

    init(socketPath: String) {
        self.socketPath = socketPath
    }

    func webView(_ webView: WKWebView, start urlSchemeTask: WKURLSchemeTask) {
        guard let url = urlSchemeTask.request.url else {
            urlSchemeTask.didFailWithError(DevmAPIURLSchemeHandlerError.missingRequestURL)
            return
        }

        guard let daemonPath = Self.daemonPath(for: url) else {
            urlSchemeTask.didFailWithError(DevmAPIURLSchemeHandlerError.unparsableRequestURL(url))
            return
        }
        let taskID = ObjectIdentifier(urlSchemeTask as AnyObject)

        UnixSocketClient.request(
            method: urlSchemeTask.request.httpMethod ?? "GET",
            socketPath: socketPath,
            path: daemonPath,
            body: urlSchemeTask.request.httpBody
        ) { [weak self] result in
            defer { self?.markTaskComplete(taskID) }
            guard let self = self, !self.isStopped(taskID) else { return }

            switch result {
            case .success(let (status, headers, body)):
                // gui.html loads from file:// origin, so fetch() to devm-api://
                // is cross-origin. WebKit needs an explicit
                // Access-Control-Allow-Origin header on the response or it
                // rejects the entire response as "Load failed" — the daemon
                // itself has no notion of CORS since its wire protocol is a
                // Unix socket, so the scheme handler synthesizes the header.
                var respHeaders = headers
                respHeaders["Access-Control-Allow-Origin"] = "*"
                guard let response = HTTPURLResponse(
                    url: url,
                    statusCode: status,
                    httpVersion: "HTTP/1.1",
                    headerFields: respHeaders
                ) else {
                    urlSchemeTask.didFailWithError(DevmAPIURLSchemeHandlerError.invalidHTTPResponse(status: status))
                    return
                }
                urlSchemeTask.didReceive(response)
                urlSchemeTask.didReceive(body)
                urlSchemeTask.didFinish()
            case .failure(let error):
                schemeLog.error("dial failed: path=\(daemonPath, privacy: .public) socket=\(self.socketPath, privacy: .public) error=\(String(describing: error), privacy: .public)")
                urlSchemeTask.didFailWithError(error)
            }
        }
    }

    func webView(_ webView: WKWebView, stop urlSchemeTask: WKURLSchemeTask) {
        let taskID = ObjectIdentifier(urlSchemeTask as AnyObject)
        lock.lock()
        stoppedTasks.insert(taskID)
        lock.unlock()
    }

    private func isStopped(_ taskID: ObjectIdentifier) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return stoppedTasks.contains(taskID)
    }

    /// Removes taskID from stoppedTasks once the UnixSocketClient completion
    /// has finished (delivered or not — the isStopped check above already
    /// happened). Without this, stoppedTasks would grow unbounded across the
    /// resident menu-bar app's process lifetime as tasks are stopped and
    /// never revisited.
    private func markTaskComplete(_ taskID: ObjectIdentifier) {
        lock.lock()
        stoppedTasks.remove(taskID)
        lock.unlock()
    }

    /// Extracts the request URL's path as the daemon-facing path,
    /// preserving the query string and dropping the fragment (URL parsing
    /// already excludes the fragment from both components). The URL's host
    /// (e.g. "vm") is a syntactic placeholder only and is never part of the
    /// daemon path.
    ///
    /// Uses the percent-*encoded* path/query (`URLComponents`), not
    /// `URL.path`/`URL.query` — those decode on read, which would corrupt
    /// the raw HTTP request line (`%20` becoming a literal space) and
    /// silently collapse an encoded `%2F` into a route-separating `/`.
    /// `percentEncodedPath` also preserves a trailing slash, which
    /// `URL.path` strips.
    static func daemonPath(for url: URL) -> String? {
        guard let comps = URLComponents(url: url, resolvingAgainstBaseURL: false) else {
            return nil
        }
        let path = comps.percentEncodedPath
        guard !path.isEmpty, path != "/" else {
            return nil
        }

        if let query = comps.percentEncodedQuery, !query.isEmpty {
            return path + "?" + query
        }
        return path
    }
}
