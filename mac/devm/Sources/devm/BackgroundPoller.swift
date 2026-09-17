import Foundation
import os.log

private let pollerLog = Logger(subsystem: "com.mdubb86.devm.mac", category: "poller")

/// Polls `/status/all` over the daemon's Unix socket on a low, fixed
/// cadence regardless of whether the main window is visible — the Svelte
/// store's 1 Hz polling only runs while the window is on screen, so a
/// hidden-window user would otherwise never see a divergence surface on
/// the menu-bar icon.
final class BackgroundPoller {
    static let interval: TimeInterval = 5.0

    private var timer: Timer?
    private let socketPath: String
    private let onUpdate: (Bool) -> Void

    init(socketPath: String, onUpdate: @escaping (Bool) -> Void) {
        self.socketPath = socketPath
        self.onUpdate = onUpdate
    }

    func start() {
        stop()
        let timer = Timer.scheduledTimer(withTimeInterval: Self.interval, repeats: true) { [weak self] _ in
            self?.tick()
        }
        self.timer = timer
        tick()
    }

    func stop() {
        timer?.invalidate()
        timer = nil
    }

    private func tick() {
        UnixSocketClient.request(
            method: "GET",
            socketPath: socketPath,
            path: "/status/all",
            body: nil
        ) { [weak self] result in
            guard let self = self else { return }
            switch result {
            case .success(let (status, _, body)):
                guard status == 200, let anyDiverged = Self.anyProjectDiverged(in: body) else {
                    pollerLog.error("unparsable /status/all response: status=\(status, privacy: .public)")
                    return
                }
                self.onUpdate(anyDiverged)
            case .failure(let error):
                // The daemon being briefly unreachable (not yet started, VM
                // restarting) is expected on this cadence; log it but never
                // treat it as "not diverged" — leave the existing badge state
                // alone rather than flapping it on a transient dial failure.
                pollerLog.error("poll failed: socket=\(self.socketPath, privacy: .public) error=\(String(describing: error), privacy: .public)")
            }
        }
    }

    private static func anyProjectDiverged(in body: Data) -> Bool? {
        guard let rows = try? JSONSerialization.jsonObject(with: body) as? [[String: Any]] else {
            return nil
        }
        return rows.contains { row in
            guard let approveState = row["approve_state"] as? [String: Any],
                  let diverged = approveState["diverged"] as? Bool else {
                return false
            }
            return diverged
        }
    }
}
