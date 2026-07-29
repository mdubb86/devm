// Package setsidshim embeds the devm-setsid-shim binary in the devm
// binary so every install method (self-update, brew, curl-and-tar)
// ships devm + devm-setsid-shim atomically. Prior layouts kept
// helper binaries as separate files next to devm; `devm upgrade`
// only replaced the devm binary and left sidecars stale (or absent
// on fresh self-updated installs).
//
// The embedded blob is gzipped, matching the iron-proxy/devm-helper
// embed pattern. Ensure() decompresses to <runtimeDir>/bin/devm-setsid-shim
// on daemon start, checksummed so a matching on-disk copy is reused.
package setsidshim

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed embed/devm-setsid-shim.gz
var setsidShimGz []byte

// embedSha256Hex is the sha256 of the embedded gzipped blob, computed
// once at process start. Ensure() writes this to a
// devm-setsid-shim.sha256 sidecar next to the extracted binary; a
// matching sidecar means the on-disk devm-setsid-shim is fresh (this
// build's copy) and skips re-extraction on subsequent daemon starts.
var embedSha256Hex = func() string {
	h := sha256.Sum256(setsidShimGz)
	return hex.EncodeToString(h[:])
}()

// EmbeddedSha256 is the hex sha256 of the devm-setsid-shim binary
// embedded in this devm build — devm's identity for "the current
// devm-setsid-shim".
func EmbeddedSha256() string { return embedSha256Hex }
