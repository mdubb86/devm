package orchestrator

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/mtwaage/devm/internal/sandbox"
)

// SnapshotPath is the in-VM location of the last-applied spec snapshot.
// User-writable (UID 1000) so no sudo is required.
const SnapshotPath = "/home/agent/.devm/applied.yaml"

// ReadSnapshot returns the snapshot's contents. If the file does not
// exist, returns ("", nil) — the absence of a snapshot is a valid state
// (first reconcile after a cold start that somehow didn't write).
func ReadSnapshot(sb *sandbox.Sandbox) (string, error) {
	out, err := sb.Runner.Output("sbx", "exec", sb.Name, "cat", SnapshotPath)
	if err != nil {
		// Heuristic: cat exits non-zero with "No such file" on missing path.
		// We accept that as "no snapshot yet"; other errors bubble up.
		msg := err.Error()
		if strings.Contains(msg, "No such file") || strings.Contains(msg, "no such file") {
			return "", nil
		}
		return "", err
	}
	return string(out), nil
}

// WriteSnapshot atomically writes content to SnapshotPath inside the VM:
// mkdir -p the directory, write to .tmp, then rename. The rename is the
// atomic step so a reader never sees a partially-written snapshot.
// WriteSnapshot writes content atomically to the in-VM snapshot path.
// Encodes content as base64 on the command line rather than piping via
// stdin: empirically, `sbx exec <name> sh -c "cat > ..."` invoked from
// Go's exec.Cmd with a strings.NewReader stdin can hang indefinitely
// (the cmd.Wait() goroutine never returns even though the same call
// in a shell completes in <2s). Base64-on-cmdline sidesteps the issue
// — the whole content is in the argv and no stdin pipe is involved.
//
// Snapshots are small (a few KB of YAML); macOS ARG_MAX (~1MB) is
// orders of magnitude larger than what we need.
func WriteSnapshot(sb *sandbox.Sandbox, content string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	cmd := fmt.Sprintf("mkdir -p /home/agent/.devm && "+
		"echo %s | base64 -d > /home/agent/.devm/applied.yaml.tmp && "+
		"mv /home/agent/.devm/applied.yaml.tmp /home/agent/.devm/applied.yaml",
		encoded)
	return sb.Runner.Run("sbx", "exec", sb.Name, "sh", "-c", cmd)
}
