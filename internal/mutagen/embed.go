// Package mutagen embeds the mutagen binary and extracts it under
// devm's runtime dir. Also carries session config helpers and the
// default-ignore list. Pure library; supervisor glue lives in
// internal/serviceapi.
package mutagen

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed embed/mutagen.gz
var mutagenGz []byte

//go:embed embed/mutagen-agents.tar.gz
var mutagenAgentsTarGz []byte

var embedSha256Hex string

func init() {
	sum := sha256.Sum256(mutagenGz)
	embedSha256Hex = hex.EncodeToString(sum[:])
}

// EmbeddedSha256 returns the hex sha256 of the embedded gzipped
// mutagen blob. Stable identity for the current build.
func EmbeddedSha256() string { return embedSha256Hex }

// EmbeddedVersion returns the mutagen release version this build
// carries.
func EmbeddedVersion() string { return "v0.18.1" }
