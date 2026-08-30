// Package tartmutagenssh embeds the compiled tart-mutagen-ssh shim
// binary. The shim is a darwin/arm64 Mac-side helper (not a guest
// binary), extracted at daemon startup to <runtime-dir>/mutagen-ssh-dir/
// and pointed at by MUTAGEN_SSH_PATH in the mutagen daemon's env.
//
// See cmd/tart-mutagen-ssh/main.go for the shim itself and
// docs/superpowers/specs/2026-08-29-mutagen-tart-transport.md for the
// broader design.
package tartmutagenssh

import (
	_ "embed"
)

//go:generate sh -c "cd ../../ && GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o internal/tartmutagenssh/embed/tart-mutagen-ssh ./cmd/tart-mutagen-ssh"

//go:embed embed/tart-mutagen-ssh
var shimBin []byte

// Bytes returns the compiled darwin/arm64 tart-mutagen-ssh binary bytes.
// Callers write them to disk, chmod +x, and set MUTAGEN_SSH_PATH.
func Bytes() []byte { return shimBin }
