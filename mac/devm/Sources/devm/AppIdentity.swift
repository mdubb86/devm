import Foundation

struct AppIdentity {
    let bundleID: String
    let socketPath: String
    let launchAgentLabel: String

    static let current: AppIdentity = {
        #if DEVM_E2E
        let name = "devm-e2e"
        let bundleID = "com.mdubb86.devm-e2e.menuapp"
        #else
        let name = "devm"
        let bundleID = "com.mdubb86.devm.menuapp"
        #endif
        let socket = NSHomeDirectory() + "/Library/Application Support/\(name)/devm.sock"
        return AppIdentity(
            bundleID: bundleID,
            socketPath: socket,
            launchAgentLabel: bundleID
        )
    }()
}
