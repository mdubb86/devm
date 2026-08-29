// Package guestbin embeds guest-side binaries devm ships into the VM
// via the provisioning bundle. Currently: pop, run.
//
// The embed is uncompressed (matches internal/docker/embed.go's runc
// shim) — the binary is small and provisioning-time extraction is
// trivial.
package guestbin

import (
	_ "embed"
)

//go:generate sh -c "cd ../../ && GOOS=linux GOARCH=arm64 go build -o internal/guestbin/embed/pop ./cmd/pop"

//go:embed embed/pop
var popBin []byte

// Pop returns the compiled linux/arm64 pop binary bytes for the
// provisioning bundle (internal/devmbundle) to write to /opt/devm/bin/pop.
func Pop() []byte { return popBin }

//go:generate sh -c "cd ../../ && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o internal/guestbin/embed/run ./cmd/run"

//go:embed embed/run
var runBin []byte

// Run returns the compiled linux/arm64 run binary bytes for the
// provisioning bundle (internal/devmbundle) to write to /opt/devm/bin/run.
func Run() []byte { return runBin }
