package docker

import (
	"fmt"
	"strings"
)

// InstallScript returns the shell script the provisioner runs inside
// the guest VM to install Docker Engine, register devm-runc-shim as
// the default OCI runtime, add the socket-permission drop-in, restart
// docker, install upstream buildkitd (pinned to BUILDKIT_VERSION in
// the script) with systemd units from the release tarball, wire its
// OCI worker at devm-runc-shim via /etc/buildkit/buildkitd.toml, and
// register the "devm" buildx builder against the buildkitd unix
// socket.
//
// The shim binaries (devm-runc-shim and docker CLI shim) are delivered
// via the devmbundle; this script assumes /usr/local/bin/devm-runc-shim
// already exists (piped in by the bundle's install.sh).
//
// Fails fast on any error (`set -e`). Never declares docker.service as
// a devm-managed service — get.docker.com enables it internally.
func InstallScript() string {
	// daemon.json content — full write, no merge. devm owns this file.
	daemon := `{
  "runtimes": {
    "devm": { "path": "/usr/local/bin/devm-runc-shim" }
  },
  "default-runtime": "devm"
}`

	socketOverride := `[Service]
ExecStartPost=/bin/chmod 666 /run/docker.sock`

	buildkitdToml := `[worker.oci]
  binary = "/usr/local/bin/devm-runc-shim"`

	var b strings.Builder
	fmt.Fprintln(&b, "set -e")
	fmt.Fprintln(&b, "set -o pipefail")
	fmt.Fprintln(&b, "# 1. Install Docker Engine via upstream installer.")
	fmt.Fprintln(&b, "curl -fsSL https://get.docker.com | sh")
	fmt.Fprintln(&b, "sudo usermod -aG docker devm")
	fmt.Fprintln(&b, "# 2. Verify real runc is where we expect. daemon.json points there.")
	fmt.Fprintln(&b, `test -x /usr/bin/runc || { echo "FAIL: /usr/bin/runc missing after docker install" >&2; exit 1; }`)
	fmt.Fprintln(&b, "# 3. Verify shim landed (piped in over stdin before this script ran).")
	fmt.Fprintln(&b, `test -x /usr/local/bin/devm-runc-shim || { echo "FAIL: /usr/local/bin/devm-runc-shim missing" >&2; exit 1; }`)
	fmt.Fprintln(&b, "# 4. Register shim as default OCI runtime.")
	fmt.Fprintln(&b, "sudo install -d /etc/docker")
	fmt.Fprintln(&b, "sudo tee /etc/docker/daemon.json > /dev/null <<'DEVM_DAEMON_JSON'")
	fmt.Fprintln(&b, daemon)
	fmt.Fprintln(&b, "DEVM_DAEMON_JSON")
	fmt.Fprintln(&b, "# 5. Socket permissions drop-in so /run/docker.sock is usable")
	fmt.Fprintln(&b, "#    inside the VM without needing a fresh login for the docker")
	fmt.Fprintln(&b, "#    group change to take effect.")
	fmt.Fprintln(&b, "sudo install -d /etc/systemd/system/docker.service.d")
	fmt.Fprintln(&b, "sudo tee /etc/systemd/system/docker.service.d/override.conf > /dev/null <<'DEVM_SOCKET_OVERRIDE'")
	fmt.Fprintln(&b, socketOverride)
	fmt.Fprintln(&b, "DEVM_SOCKET_OVERRIDE")
	fmt.Fprintln(&b, "# 6. Reload systemd + restart docker so the drop-in + daemon.json apply.")
	fmt.Fprintln(&b, "sudo systemctl daemon-reload")
	fmt.Fprintln(&b, "sudo systemctl restart docker")
	fmt.Fprintln(&b, "# 7. Fetch upstream buildkitd tarball (ships systemd units in-tree).")
	fmt.Fprintln(&b, "BUILDKIT_VERSION=v0.28.1")
	fmt.Fprintln(&b, `curl -fsSL "https://github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/buildkit-${BUILDKIT_VERSION}.linux-arm64.tar.gz" \`)
	fmt.Fprintln(&b, "  | sudo tar -xz -C /usr/local")
	fmt.Fprintln(&b, "# 8. Install unmodified upstream systemd units.")
	fmt.Fprintln(&b, "sudo cp /usr/local/examples/systemd/system/buildkit.service /etc/systemd/system/")
	fmt.Fprintln(&b, "sudo cp /usr/local/examples/systemd/system/buildkit.socket  /etc/systemd/system/")
	fmt.Fprintln(&b, "# 9. Configure buildkitd's OCI worker to use devm-runc-shim.")
	fmt.Fprintln(&b, "sudo install -d /etc/buildkit")
	fmt.Fprintln(&b, "sudo tee /etc/buildkit/buildkitd.toml >/dev/null <<'DEVM_BUILDKITD_TOML'")
	fmt.Fprintln(&b, buildkitdToml)
	fmt.Fprintln(&b, "DEVM_BUILDKITD_TOML")
	fmt.Fprintln(&b, "# 10. Enable + start via socket activation.")
	fmt.Fprintln(&b, "sudo systemctl daemon-reload")
	fmt.Fprintln(&b, "sudo systemctl enable --now buildkit.socket")
	fmt.Fprintln(&b, "# 11. Idempotent buildx builder registration.")
	fmt.Fprintln(&b, "if ! docker buildx inspect devm >/dev/null 2>&1; then")
	fmt.Fprintln(&b, "  docker buildx create \\")
	fmt.Fprintln(&b, "    --driver remote \\")
	fmt.Fprintln(&b, "    --name devm \\")
	fmt.Fprintln(&b, "    --use \\")
	fmt.Fprintln(&b, "    unix:///run/buildkit/buildkitd.sock")
	fmt.Fprintln(&b, "fi")
	return b.String()
}
