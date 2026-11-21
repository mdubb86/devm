// Package guestbin embeds the guest-side devm dispatcher (gdevm) that
// devm ships into the VM via the provisioning bundle.
//
// The embed is uncompressed (matches internal/docker/embed.go's runc
// shim) — the binary is small and provisioning-time extraction is
// trivial.
package guestbin

import (
	_ "embed"
)

//go:generate sh -c "cd ../../ && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o internal/guestbin/embed/gdevm ./cmd/gdevm"

//go:embed embed/gdevm
var gdevmBin []byte

// Gdevm returns the compiled linux/arm64 gdevm binary bytes for the
// provisioning bundle (internal/devmbundle) to write to
// /opt/devm/bin/gdevm. The install.sh bin/* loop then symlinks it to
// /usr/local/bin/gdevm. Subcommands: pop, propose, run.
func Gdevm() []byte { return gdevmBin }
