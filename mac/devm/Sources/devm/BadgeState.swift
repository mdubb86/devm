import Combine

/// Shared observable flag surfaced by BackgroundPoller and rendered by the
/// MenuBarExtra icon. Only one source (BackgroundPoller) writes to it, on
/// the main queue, per the app's single-write-path convention.
final class BadgeState: ObservableObject {
    static let shared = BadgeState()

    @Published var anyDiverged: Bool = false

    private init() {}
}
