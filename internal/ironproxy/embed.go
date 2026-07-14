// Package ironproxy embeds the iron-proxy binary in the devm binary
// so every install method (self-update, brew, curl-and-tar) ships
// devm + iron-proxy atomically. Prior layouts kept iron-proxy as a
// separate file next to devm; `devm upgrade` only replaced the devm
// binary and left iron-proxy stale (or absent on fresh self-updated
// installs).
//
// The embedded blob is gzipped — iron-proxy compresses ~3.3× (50 MB
// → 15 MB) so the devm binary stays reasonable in size. Ensure()
// decompresses to <runtimeDir>/bin/iron-proxy on daemon start,
// checksummed so a matching on-disk copy is reused.
package ironproxy

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed embed/iron-proxy.gz
var ironProxyGz []byte

// embedSha256Hex is the sha256 of the embedded gzipped blob, computed
// once at process start. Ensure() writes this to an
// iron-proxy.sha256 sidecar next to the extracted binary; a matching
// sidecar means the on-disk iron-proxy is fresh (this build's copy)
// and skips re-extraction on subsequent daemon starts.
var embedSha256Hex = func() string {
	h := sha256.Sum256(ironProxyGz)
	return hex.EncodeToString(h[:])
}()
