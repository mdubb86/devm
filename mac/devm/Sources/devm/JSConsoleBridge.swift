import Foundation
import WebKit
import os.log

// Bridge that pipes the webview's console.{log,info,warn,error,debug}
// calls to Swift's os.log under subsystem
// "com.mdubb86.devm.mac" / category "jsconsole" so JS-side errors show
// up in `log show --predicate 'subsystem == "com.mdubb86.devm.mac"'`
// without attaching a Web Inspector.
enum JSConsoleBridge {
    private static let logger = Logger(subsystem: "com.mdubb86.devm.mac", category: "jsconsole")

    static func install(on configuration: WKWebViewConfiguration) {
        let handler = MessageHandler()
        configuration.userContentController.add(handler, name: "devmConsole")

        let script = WKUserScript(
            source: injectionSource,
            injectionTime: .atDocumentStart,
            forMainFrameOnly: true
        )
        configuration.userContentController.addUserScript(script)
    }

    // Overrides console.{log,info,warn,error,debug} and window.onerror
    // to forward every call as one JSON payload through the
    // `devmConsole` message handler.
    private static let injectionSource = """
    (function () {
      const forward = (level, args) => {
        try {
          const payload = Array.from(args, (a) => {
            if (a instanceof Error) return a.stack || (a.name + ': ' + a.message);
            if (typeof a === 'string') return a;
            try { return JSON.stringify(a); } catch (_) { return String(a); }
          }).join(' ');
          window.webkit.messageHandlers.devmConsole.postMessage({ level, message: payload });
        } catch (_) { /* never let logging break the page */ }
      };
      ['log','info','warn','error','debug'].forEach((level) => {
        const original = console[level] ? console[level].bind(console) : null;
        console[level] = function () { forward(level, arguments); if (original) original.apply(console, arguments); };
      });
      window.addEventListener('error', (e) => {
        forward('error', [(e.message || 'window.error') + ' at ' + (e.filename || '?') + ':' + (e.lineno || 0)]);
      });
      window.addEventListener('unhandledrejection', (e) => {
        const reason = e.reason && (e.reason.stack || e.reason.message || String(e.reason));
        forward('error', ['unhandledrejection: ' + (reason || 'unknown')]);
      });
    })();
    """

    private final class MessageHandler: NSObject, WKScriptMessageHandler {
        func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
            guard message.name == "devmConsole",
                  let body = message.body as? [String: Any],
                  let level = body["level"] as? String,
                  let text = body["message"] as? String else { return }
            switch level {
            case "error":
                JSConsoleBridge.logger.error("\(text, privacy: .public)")
            case "warn":
                JSConsoleBridge.logger.warning("\(text, privacy: .public)")
            case "info":
                JSConsoleBridge.logger.info("\(text, privacy: .public)")
            case "debug":
                JSConsoleBridge.logger.debug("\(text, privacy: .public)")
            default:
                JSConsoleBridge.logger.log("\(text, privacy: .public)")
            }
        }
    }
}
