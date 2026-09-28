// Package guestbin embeds the guest-side devm dispatcher (devmg) that
// devm ships into the VM via the provisioning bundle.
//
// The embed is uncompressed (matches internal/docker/embed.go's runc
// shim) — the binary is small and provisioning-time extraction is
// trivial.
package guestbin

import (
	_ "embed"
)

//go:generate sh -c "cd ../../ && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o internal/guestbin/embed/devmg ./cmd/devmg"

//go:embed embed/devmg
var devmgBin []byte

// Devmg returns the compiled linux/arm64 devmg binary bytes for the
// provisioning bundle (internal/devmbundle) to write to
// /opt/devm/bin/devmg. The install.sh bin/* loop then symlinks it to
// /usr/local/bin/devmg. Subcommands: pop, propose, run.
func Devmg() []byte { return devmgBin }
