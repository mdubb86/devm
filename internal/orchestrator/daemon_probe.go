package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// DaemonStatus is what `devm status` reports about the devm daemon —
// separate from any per-project sandbox state so it's meaningful even
// outside a project directory.
//
// When the daemon is running, Fingerprint comes from its /version
// reply. When the daemon is stopped, we shell out to the LaunchDaemon
// plist's program path with `version --json` — reading the on-disk
// binary's compiled-in constant costs no privileges and no sockets.
// Either way, we can compare against the CLI's own compiled-in
// Fingerprint and report drift accurately.
type DaemonStatus struct {
	// Running is true when the daemon responds to /health over the
	// unix socket.
	Running bool

	// Installed is true when the LaunchDaemon plist exists and points
	// at an executable file. Distinct from Running because a stopped-
	// but-installed daemon is a legitimate state (`devm service stop`,
	// LaunchDaemon crash, first-time-since-boot).
	Installed bool

	// BinaryPath is what launchctl print reports as the daemon's
	// program. Empty when Installed is false.
	BinaryPath string

	// Fingerprint is the on-disk daemon binary's compiled-in
	// Fingerprint constant (from /version when Running, from
	// `<path> version --json` when stopped). Empty when we can't
	// determine it.
	Fingerprint string

	// FingerprintMatchesCLI is true iff Fingerprint equals the CLI's
	// own compiled-in Fingerprint — meaning both processes were
	// compiled from the same build. False means the on-disk binary
	// has been rebuilt since the daemon last shipped: user should
	// run `devm install`.
	FingerprintMatchesCLI bool

	// Error captures why we couldn't fully probe (missing plist,
	// unreadable binary, exec timeout, malformed JSON). Populated when
	// Fingerprint stays empty; empty on success.
	Error string
}

// ProbeDaemon inspects the daemon and returns its status without
// requiring any project context or privileges. cliFingerprint is the
// CLI's own compiled-in Fingerprint constant — passed in rather than
// read from a package var because orchestrator doesn't import cmd/devm.
func ProbeDaemon(ctx context.Context, cfg identity.Config, cliFingerprint string) DaemonStatus {
	st := DaemonStatus{}

	// First: is it running? Try /health. Cheap when the daemon is up,
	// fast fail when it's down.
	healthCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c := serviceapi.NewClient(cfg)
	if err := c.Health(healthCtx); err == nil {
		st.Running = true
		// Ask the running daemon for its Fingerprint via /version.
		if b, err := c.BuildInfo(healthCtx); err == nil {
			st.Fingerprint = b.Fingerprint
		}
	}

	// Read the LaunchDaemon plist regardless of Running — an installed-
	// but-stopped daemon needs the binary path so we can shell out for
	// the Fingerprint.
	path, plistErr := daemonBinaryPathFromLaunchctl(ctx, cfg)
	if plistErr == nil {
		st.Installed = true
		st.BinaryPath = path
	}

	// If we already have the Fingerprint from /version, we're done.
	if st.Fingerprint == "" && st.Installed {
		if fp, err := readFingerprintFromBinary(ctx, path); err == nil {
			st.Fingerprint = fp
		} else {
			st.Error = "read on-disk fingerprint: " + err.Error()
		}
	}

	if st.Fingerprint != "" && cliFingerprint != "" {
		st.FingerprintMatchesCLI = st.Fingerprint == cliFingerprint
	}
	if !st.Running && !st.Installed && st.Error == "" {
		st.Error = "daemon not installed (run `devm install`)"
	}
	return st
}

// daemonBinaryPathFromLaunchctl parses `launchctl print
// system/com.devm.service` (or the e2e equivalent target) output and
// extracts the plist's `program` field. Read-only query — no
// privileges required. Returns an error when the service is unknown
// to launchd.
func daemonBinaryPathFromLaunchctl(ctx context.Context, cfg identity.Config) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "launchctl", "print", cfg.LaunchdTargetDaemon()).Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "program = ") {
			return strings.TrimPrefix(trimmed, "program = "), nil
		}
	}
	return "", errors.New("no `program = ` line in launchctl print output")
}

// readFingerprintFromBinary invokes the given binary with
// `version --json` and returns the fingerprint field. Runs as the
// current user — no sudo, no launchd interaction. The binary just
// prints its compiled-in constants.
func readFingerprintFromBinary(ctx context.Context, path string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, path, "version", "--json").Output()
	if err != nil {
		return "", err
	}
	var v struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", err
	}
	return v.Fingerprint, nil
}
