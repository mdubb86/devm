// Embed integration for the devm-helper binary. devm-helper is a
// separate binary (cmd/devm-helper/main.go) that runs as a root
// LaunchDaemon; embedding it here means every install method (self-
// update, brew, tarball) ships devm + devm-helper atomically. Prior
// layouts kept devm-helper as a separate goreleaser build; `devm
// upgrade` only replaced the devm binary and left devm-helper stale.
//
// The embedded blob is gzipped — devm-helper compresses ~2.5× (4 MB
// → ~1.5 MB). Extract() decompresses to a caller-provided targetPath
// on install, checksummed via a sidecar so a matching on-disk copy is
// reused across re-runs of `devm install`.
package helper

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed embed/devm-helper.gz
var devmHelperGz []byte

var embedSha256Hex = func() string {
	h := sha256.Sum256(devmHelperGz)
	return hex.EncodeToString(h[:])
}()

// EmbeddedSha256 is the hex sha256 of the gzipped devm-helper blob
// embedded in this devm build. Used to stamp <targetPath>.sha256 so
// subsequent Extract() calls can short-circuit when the on-disk copy
// is already this build's.
func EmbeddedSha256() string { return embedSha256Hex }
