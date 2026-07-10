package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// InstallScript returns the shell script the provisioner runs inside
// the guest VM to install Docker Engine, register devm-runc-shim as
// the default OCI runtime, add the socket-permission drop-in, add the
// host→container reachability nftables rule, and restart docker.
//
// workspaceVMPath is the guest-side absolute path where the workspace
// is mounted; the shim lives at <workspaceVMPath>/.devm/scripts/devm-runc-shim
// (see WriteShim).
//
// Fails fast on any error (`set -e`). Never declares docker.service as
// a devm-managed service — get.docker.com enables it internally.
func InstallScript(workspaceVMPath string) string {
	// daemon.json content — full write, no merge. devm owns this file.
	daemon := `{
  "runtimes": {
    "devm": { "path": "/usr/local/bin/devm-runc-shim" }
  },
  "default-runtime": "devm"
}`

	socketOverride := `[Service]
ExecStartPost=/bin/chmod 666 /run/docker.sock`

	shimSrc := workspaceVMPath + "/.devm/scripts/devm-runc-shim"

	var b strings.Builder
	fmt.Fprintln(&b, "set -e")
	fmt.Fprintln(&b, "# 1. Install Docker Engine via upstream installer.")
	fmt.Fprintln(&b, "curl -fsSL https://get.docker.com | sh")
	fmt.Fprintln(&b, "sudo usermod -aG docker devm")
	fmt.Fprintln(&b, "# 2. Verify real runc is where we expect. daemon.json points there.")
	fmt.Fprintln(&b, `test -x /usr/bin/runc || { echo "FAIL: /usr/bin/runc missing after docker install" >&2; exit 1; }`)
	fmt.Fprintln(&b, "# 3. Install devm-runc-shim from the workspace mount.")
	fmt.Fprintf(&b, "sudo install -m 0755 %q /usr/local/bin/devm-runc-shim\n", shimSrc)
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
	fmt.Fprintln(&b, "# 6. Host→container reachability. Docker DNAT's published-port")
	fmt.Fprintln(&b, "#    traffic to a 172.x.x.x bridge address; this rule lets our")
	fmt.Fprintln(&b, "#    filter chain accept it. Snapshotted by apply-egress-enforcement.")
	fmt.Fprintln(&b, "sudo nft add rule inet devm_filter user_output ip daddr 172.16.0.0/12 accept")
	fmt.Fprintln(&b, "# 7. Reload systemd + restart docker so the drop-in + daemon.json apply.")
	fmt.Fprintln(&b, "sudo systemctl daemon-reload")
	fmt.Fprintln(&b, "sudo systemctl restart docker")
	return b.String()
}

// Install is the entrypoint the provisioner calls to run the docker
// feature step inside the guest VM. execShell writes the script's
// stdout+stderr to w and returns non-nil on non-zero exit.
type shellExecutor interface {
	ExecShell(ctx context.Context, w io.Writer, script string) error
}

func Install(ctx context.Context, w io.Writer, sh shellExecutor, workspaceVMPath string) error {
	return sh.ExecShell(ctx, w, InstallScript(workspaceVMPath))
}

// WriteShim writes the embedded shim binary to
// <repoRoot>/.devm/scripts/devm-runc-shim (mode 0755). Called from the
// cold-start path when cfg.Docker == true so the shim is present under
// the workspace mount before the docker-feature provisioner step runs.
func WriteShim(repoRoot string) error {
	// Directory is created by render.WriteDevmDir which runs before this;
	// we only need to drop the binary.
	return writeShimBinary(repoRoot, Shim())
}
