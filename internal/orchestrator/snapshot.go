package orchestrator

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/mdubb86/devm/internal/sandbox/tart"
)

// SnapshotFilename is the file name of the last-applied spec snapshot,
// stored under the guest user's home directory inside the VM. The path
// is resolved at runtime via `$HOME` inside the tart exec so it works
// regardless of the guest user's login name.
const SnapshotFilename = ".devm/applied.yaml"

// ReadSnapshot returns the snapshot's contents. If the file does not
// exist, returns ("", nil) — the absence of a snapshot is a valid state
// (first reconcile after a cold start that somehow didn't write).
func ReadSnapshot(tr *tart.Tart, vmName string) (string, error) {
	r := tr.ExecWithRetry(context.Background(), vmName, []string{
		"bash", "-c", "cat \"$HOME/" + SnapshotFilename + "\"",
	})
	if r.ExitCode != 0 {
		if strings.Contains(r.Stderr, "No such file") || strings.Contains(r.Stderr, "no such file") {
			return "", nil
		}
		return "", fmt.Errorf("read snapshot: exit %d (stderr: %s)", r.ExitCode, r.Stderr)
	}
	return r.Stdout, nil
}

// WriteSnapshot writes content atomically to the in-VM snapshot path
// under the guest user's home directory. Encodes content as base64 on
// the command line to avoid stdin pipe issues; all content is in argv
// and no stdin pipe is involved.
//
// Snapshots are small (a few KB of YAML); macOS ARG_MAX (~1MB) is
// orders of magnitude larger than what we need.
func WriteSnapshot(tr *tart.Tart, vmName, content string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	cmd := fmt.Sprintf(
		`mkdir -p "$HOME/.devm" && `+
			`echo %s | base64 -d > "$HOME/%s.tmp" && `+
			`mv "$HOME/%s.tmp" "$HOME/%s"`,
		encoded, SnapshotFilename, SnapshotFilename, SnapshotFilename,
	)
	r := tr.ExecWithRetry(context.Background(), vmName, []string{"bash", "-c", cmd})
	if r.ExitCode != 0 {
		return fmt.Errorf("write snapshot: exit %d (stderr: %s)", r.ExitCode, r.Stderr)
	}
	return nil
}
