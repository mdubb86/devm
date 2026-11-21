package serviceapi

// CurrentBundleFingerprint returns the identity of the bundle
// contents the daemon would ship right now. Coarse: derived from
// the compile-time daemon Fingerprint (already a random per-build
// stamp injected via goreleaser's ldflags in .goreleaser.yaml), so
// any prod daemon binary swap flips this without additional hashing.
//
// Dev daemons compiled with the default Fingerprint = "dev" always
// return "dev" here — deliberate, so local iteration does not fire
// a bundle pipe after every rebuild.
//
// Returns empty when the cache hasn't been given a Build yet
// (an early-startup query before cache.SetBuild fires). Empty
// compares different from any real Fingerprint, so a spurious
// early-refresh is avoided by the caller checking for empty first.
func CurrentBundleFingerprint(cache *StateCache) string {
	return cache.Global().Build.Fingerprint
}
