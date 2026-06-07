package orchestrator

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
)

// FailureReport describes the first failing step in a phase
// (install or startup). Produced by readPhaseFailure when a phase
// gate's sentinel didn't appear in time.
type FailureReport struct {
	Phase        string // "install" or "startup"
	StepN        int    // 1-based index of the failing step
	RC           int    // user cmd's rc; -1 if step never wrote .rc (hung)
	UserCmd      string // human-readable command text for the error message
	CapturedTail string // last ~4 KB of <phase>-<N>/current
	Truncated    bool
	Hung         bool // true if step has neither .ok/.spawned nor .rc
}

// captureTailBytes is the max bytes of captured output included in
// FailureReport.CapturedTail. The full log lives in /tmp/.devm/<dir>.
const captureTailBytes = 4 * 1024

// readPhaseFailure walks /tmp/.devm/<phase>-* marker files via sbx
// exec to identify the first failing step and pull its captured log.
// Returns nil report if everything looks ok (shouldn't happen — the
// caller only invokes this on missing sentinel — but defensively
// returns nil to signal "couldn't find a specific cause").
func readPhaseFailure(sb *sandbox.Sandbox, phase string, cfg schema.Config) (*FailureReport, error) {
	// List markers.
	lsOut, err := sb.Runner.Output("sbx", "exec", sb.Name, "ls", "/tmp/.devm/")
	if err != nil {
		return nil, fmt.Errorf("readPhaseFailure: ls /tmp/.devm/: %w", err)
	}
	entries := strings.Split(strings.TrimSpace(string(lsOut)), "\n")
	okSet := make(map[int]bool)
	rcSet := make(map[int]bool)
	spawnedSet := make(map[int]bool)
	maxN := 0
	prefix := phase + "-"
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if !strings.HasPrefix(e, prefix) {
			continue
		}
		body := strings.TrimPrefix(e, prefix)
		// Parse <N>.ok / <N>.rc / <N>.spawned (ignore directories like <N>/).
		var n int
		var suf string
		dot := strings.Index(body, ".")
		if dot <= 0 {
			continue
		}
		if _, err := fmt.Sscanf(body[:dot], "%d", &n); err != nil {
			continue
		}
		suf = body[dot+1:]
		if n > maxN {
			maxN = n
		}
		switch suf {
		case "ok":
			okSet[n] = true
		case "rc":
			rcSet[n] = true
		case "spawned":
			spawnedSet[n] = true
		}
	}

	// Walk in order: first step that lacks .ok/.spawned is the offender.
	// If it has .rc but no .ok -> rc!=0 failure. If it lacks .rc -> hung.
	stepNs := make([]int, 0)
	for n := range okSet {
		stepNs = append(stepNs, n)
	}
	for n := range rcSet {
		stepNs = append(stepNs, n)
	}
	for n := range spawnedSet {
		stepNs = append(stepNs, n)
	}
	sort.Ints(stepNs)

	var failN int
	for n := 1; n <= maxN+1; n++ {
		if okSet[n] || spawnedSet[n] {
			continue
		}
		failN = n
		break
	}
	if failN == 0 {
		return nil, nil // all markers look ok — couldn't identify
	}

	report := &FailureReport{Phase: phase, StepN: failN}

	if rcSet[failN] {
		// Step ran to completion with non-zero rc.
		rcRaw, err := sb.Runner.Output("sbx", "exec", sb.Name, "cat",
			fmt.Sprintf("/tmp/.devm/%s-%d.rc", phase, failN))
		if err != nil {
			return nil, fmt.Errorf("readPhaseFailure: cat .rc for step %d: %w", failN, err)
		}
		rc, perr := strconv.Atoi(strings.TrimSpace(string(rcRaw)))
		if perr != nil {
			return nil, fmt.Errorf("readPhaseFailure: parse rc %q: %w", rcRaw, perr)
		}
		report.RC = rc
	} else {
		// No .rc at all → step is hung (didn't reach the wrapper's exit).
		report.RC = -1
		report.Hung = true
	}

	// Pull captured tail.
	curRaw, err := sb.Runner.Output("sbx", "exec", sb.Name, "cat",
		fmt.Sprintf("/tmp/.devm/%s-%d/current", phase, failN))
	if err == nil {
		full := string(curRaw)
		if len(full) > captureTailBytes {
			report.CapturedTail = full[len(full)-captureTailBytes:]
			report.Truncated = true
		} else {
			report.CapturedTail = full
		}
	}

	// Resolve user command text from cfg for the error message.
	report.UserCmd = resolveUserCmdText(phase, failN, cfg)

	return report, nil
}

// resolveUserCmdText returns a human-friendly description of step N's
// command, indexing into cfg.Install (install phase) or service startup
// commands (startup phase), accounting for the built-in step offsets:
//
//	install:   step 0 = cleanup, 1 = bootstrap.sh, 2..N+1 = user[0..N-1]
//	startup:   step 0 = cleanup, 1 = init-volumes, 2 = install-templates,
//	           3..M+2 = user (across services in sorted-name + decl order)
func resolveUserCmdText(phase string, stepN int, cfg schema.Config) string {
	if phase == "install" {
		switch stepN {
		case 0:
			return "(cleanup)"
		case 1:
			return "bootstrap.sh"
		}
		// step N corresponds to cfg.Install[N-2] per the render:
		// step 0 = cleanup, step 1 = bootstrap.sh, step 2..N+1 = user.
		userIdx := stepN - 2
		if userIdx >= 0 && userIdx < len(cfg.Install) {
			return cfg.Install[userIdx]
		}
		return fmt.Sprintf("(unknown install step %d)", stepN)
	}
	// startup
	switch stepN {
	case 0:
		return "(cleanup)"
	case 1:
		return "init-volumes.sh"
	case 2:
		return "install-templates.sh"
	}
	// User startup: walk services in sorted name order, decl order within.
	userIdx := stepN - 3
	names := make([]string, 0, len(cfg.Services))
	for n := range cfg.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	count := 0
	for _, name := range names {
		for _, s := range cfg.Services[name].Startup {
			if count == userIdx {
				return fmt.Sprintf("%s: %s", name, strings.Join(s.Command, " "))
			}
			count++
		}
	}
	return fmt.Sprintf("(unknown startup step %d)", stepN)
}

// formatFailureReport renders a FailureReport as the user-facing
// error message body. The shape is intentionally plain — no
// prescriptive "fix this" or "investigate with X" hints. Just the
// facts.
func formatFailureReport(r *FailureReport) string {
	var b strings.Builder
	if r.Hung {
		b.WriteString(fmt.Sprintf(
			"error: %s did not complete\n", r.Phase))
		b.WriteString(fmt.Sprintf(
			"  step %d (%s) still running or hung\n", r.StepN, r.UserCmd))
	} else {
		b.WriteString(fmt.Sprintf(
			"error: %s step %d failed (rc=%d)\n", r.Phase, r.StepN, r.RC))
		b.WriteString(fmt.Sprintf(
			"  command: %s\n", r.UserCmd))
	}
	b.WriteString(fmt.Sprintf(
		"  output (last %d bytes of /tmp/.devm/%s-%d/current):\n",
		len(r.CapturedTail), r.Phase, r.StepN))
	for _, line := range strings.Split(strings.TrimRight(r.CapturedTail, "\n"), "\n") {
		b.WriteString("    ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	if r.Truncated {
		b.WriteString("  (output truncated; full log in /tmp/.devm/" +
			r.Phase + "-" + strconv.Itoa(r.StepN) + "/current)\n")
	}
	return b.String()
}

// Default phase-gate timeouts. Test overrides via
// DEVM_INSTALL_GATE_TIMEOUT_S and DEVM_STARTUP_GATE_TIMEOUT_S.
const (
	defaultInstallGateTimeout = 120 * time.Second
	defaultStartupGateTimeout = 30 * time.Second
	defaultGatePollInterval   = 1 * time.Second
)

// waitForPhaseSentinel polls /tmp/.devm/<phase>-all-ok via sbx exec
// until present or timeout. Returns a wrapped error on timeout
// (caller pairs it with readPhaseFailure to surface what happened).
func waitForPhaseSentinel(sb *sandbox.Sandbox, phase string, timeout, poll time.Duration) error {
	sentinel := fmt.Sprintf("/tmp/.devm/%s-all-ok", phase)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := sb.Runner.Output("sbx", "exec", sb.Name, "test", "-f", sentinel); err == nil {
			return nil
		}
		time.Sleep(poll)
	}
	return fmt.Errorf("%s did not complete within %s", phase, timeout)
}

// gateTimeoutFromEnv returns the timeout for the given phase, honoring
// the DEVM_<PHASE>_GATE_TIMEOUT_S env var override if set (test hook).
func gateTimeoutFromEnv(phase string, defaultD time.Duration) time.Duration {
	key := "DEVM_" + strings.ToUpper(phase) + "_GATE_TIMEOUT_S"
	if v := os.Getenv(key); v != "" {
		if s, err := strconv.Atoi(v); err == nil {
			return time.Duration(s) * time.Second
		}
	}
	return defaultD
}
