// Package docker owns the first-class docker feature: install of Docker
// Engine, registration of devm-runc-shim as the default OCI runtime,
// socket-permission drop-in, host→container reachability, and implicit
// Docker Hub allowlist.
package docker

import _ "embed"

//go:generate sh -c "cd ../../ && GOOS=linux GOARCH=arm64 go build -o internal/docker/embed/devm-runc-shim ./cmd/devm-runc-shim"

//go:embed embed/devm-runc-shim
var shimBinary []byte

// Shim returns the bytes of the compiled linux/arm64 devm-runc-shim.
// Provisioner writes these to /usr/local/bin/devm-runc-shim in the guest.
func Shim() []byte { return shimBinary }
