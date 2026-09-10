import Foundation
import WebKit

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
/// host="vm" and path="/status/all" — those are recombined into the
/// daemon path "/vm/status/all?project=x".
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
            guard let self = self, !self.isStopped(taskID) else { return }

            switch result {
            case .success(let (status, headers, body)):
                guard let response = HTTPURLResponse(
                    url: url,
                    statusCode: status,
                    httpVersion: "HTTP/1.1",
                    headerFields: headers
                ) else {
                    urlSchemeTask.didFailWithError(DevmAPIURLSchemeHandlerError.invalidHTTPResponse(status: status))
                    return
                }
                urlSchemeTask.didReceive(response)
                urlSchemeTask.didReceive(body)
                urlSchemeTask.didFinish()
            case .failure(let error):
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

    /// Combines the request URL's host and path into the daemon-facing
    /// path, preserving the query string and dropping the fragment (URL
    /// parsing already excludes the fragment from both components).
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
        let host = comps.host ?? ""
        let encodedPath = comps.percentEncodedPath
        let path: String
        if host.isEmpty {
            path = encodedPath.isEmpty ? "/" : encodedPath
        } else {
            path = "/" + host + encodedPath
        }

        if let query = comps.percentEncodedQuery, !query.isEmpty {
            return path + "?" + query
        }
        return path
    }
}
