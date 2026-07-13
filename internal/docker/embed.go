// Package docker owns the first-class docker feature: install of Docker
// Engine, registration of devm-runc-shim as the default OCI runtime,
// socket-permission drop-in, host→container reachability, and implicit
// Docker Hub allowlist.
package docker

import _ "embed"

//go:generate sh -c "cd ../../ && GOOS=linux GOARCH=arm64 go build -o internal/docker/embed/devm-runc-shim ./cmd/devm-runc-shim"
//go:generate sh -c "cd ../../ && GOOS=linux GOARCH=arm64 go build -o internal/docker/embed/devm-docker-shim ./cmd/devm-docker-shim"

//go:embed embed/devm-runc-shim
var shimBinary []byte

//go:embed embed/devm-docker-shim
var dockerShimBinary []byte

// Shim returns the bytes of the compiled linux/arm64 devm-runc-shim.
// Provisioner writes these to /usr/local/bin/devm-runc-shim in the guest.
func Shim() []byte { return shimBinary }

// DockerShim returns the bytes of the compiled linux/arm64
// devm-docker-shim. Provisioner writes these to /usr/local/bin/docker
// in the guest so `docker build` invocations auto-inject the
// buildkit secret that mounts devm's CA into RUN steps.
func DockerShim() []byte { return dockerShimBinary }
