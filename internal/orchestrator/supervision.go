package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pterm/pterm"

	"github.com/mdubb86/devm/internal/sandbox"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/status"
)

// ErrAnchorDied signals that the anchor process exited during a phase
// gate's poll. The caller should switch to readPhaseFailureFromHost
// (the host-side mirror that wrap-fg.sh wrote before the wrapper exited).
var ErrAnchorDied = errors.New("anchor died during phase gate")

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
// FailureReport.CapturedTail. The full log lives in /tmp/.devm-<phase>/<phase>-<N>/.
const captureTailBytes = 4 * 1024

// readPhaseFailure walks /tmp/.devm-<phase>/ marker files via sbx
// exec to identify the first failing step and pull its captured log.
// Returns nil report if everything looks ok (shouldn't happen — the
// caller only invokes this on missing sentinel — but defensively
// returns nil to signal "couldn't find a specific cause").
func readPhaseFailure(sb *sandbox.Sandbox, phase string, cfg schema.Config) (*FailureReport, error) {
	// List markers.
	lsOut, err := sb.Runner.Output("sbx", "exec", sb.Name, "ls", fmt.Sprintf("/tmp/.devm-%s/", phase))
	if err != nil {
		return nil, fmt.Errorf("readPhaseFailure: ls /tmp/.devm-%s/: %w", phase, err)
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
			fmt.Sprintf("/tmp/.devm-%s/%s-%d.rc", phase, phase, failN))
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
		fmt.Sprintf("/tmp/.devm-%s/%s-%d/current", phase, phase, failN))
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
//	install:   step 1 = bootstrap.sh, 2..N+1 = user[0..N-1]
//	startup:   step 0 = cleanup, 1 = init-volumes, 2 = install-templates,
//	           3..M+2 = user (across services in sorted-name + decl order)
func resolveUserCmdText(phase string, stepN int, cfg schema.Config) string {
	if phase == "install" {
		if stepN == 1 {
			return "bootstrap.sh"
		}
		// step N corresponds to cfg.Install[N-2] per the render:
		// step 1 = bootstrap.sh, step 2..N+1 = user.
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
// facts. The "error:" header line is colored red on TTY (pterm
// auto-detects and degrades to plain on non-TTY / NO_COLOR).
func formatFailureReport(r *FailureReport) string {
	var b strings.Builder
	if r.Hung {
		b.WriteString(pterm.FgRed.Sprintf(
			"error: %s did not complete", r.Phase))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf(
			"  step %d (%s) still running or hung\n", r.StepN, r.UserCmd))
	} else {
		b.WriteString(pterm.FgRed.Sprintf(
			"error: %s step %d failed (rc=%d)", r.Phase, r.StepN, r.RC))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf(
			"  command: %s\n", r.UserCmd))
	}
	b.WriteString(fmt.Sprintf(
		"  output (last %d bytes of /tmp/.devm-%s/%s-%d/current):\n",
		len(r.CapturedTail), r.Phase, r.Phase, r.StepN))
	for _, line := range strings.Split(strings.TrimRight(r.CapturedTail, "\n"), "\n") {
		b.WriteString("    ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	if r.Truncated {
		b.WriteString("  (output truncated; full log in /tmp/.devm-" +
			r.Phase + "/" + r.Phase + "-" + strconv.Itoa(r.StepN) + "/current)\n")
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

// waitForPhaseSentinel polls /tmp/.devm-<phase>/<phase>-all-ok via sbx exec
// until present or timeout. Returns ErrAnchorDied if runDone fires before
// the sentinel appears. Returns a wrapped timeout error otherwise.
// runDone may be nil — in that case anchor-death detection is skipped.
// reporter and cfg drive per-step progress announcements; pass nil reporter
// to disable (e.g. in tests that don't care about UX).
func waitForPhaseSentinel(
	sb *sandbox.Sandbox, phase string, runDone <-chan struct{},
	timeout, poll time.Duration,
	reporter status.Reporter, cfg schema.Config,
) error {
	sentinel := fmt.Sprintf("/tmp/.devm-%s/%s-all-ok", phase, phase)
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	// Announce step 1 of the phase immediately.
	announceStep(reporter, phase, 1, cfg)
	lastAnnouncedN := 1

	for {
		// Non-blocking check for anchor death.
		if runDone != nil {
			select {
			case <-runDone:
				return ErrAnchorDied
			default:
			}
		}
		if _, err := sb.Runner.Output("sbx", "exec", sb.Name, "test", "-f", sentinel); err == nil {
			return nil
		}
		// Detect newly-completed steps via .ok markers and announce the next step.
		if reporter != nil {
			highestOk := detectHighestOk(sb, phase)
			if highestOk >= lastAnnouncedN {
				nextN := highestOk + 1
				announceStep(reporter, phase, nextN, cfg)
				lastAnnouncedN = nextN
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not complete within %s", phase, timeout)
		}
		// Sleep until next poll tick or anchor death.
		if runDone != nil {
			select {
			case <-ticker.C:
			case <-runDone:
				return ErrAnchorDied
			}
		} else {
			<-ticker.C
		}
	}
}

// announceStep calls reporter.Step with the right desc + counted flag
// for the (phase, stepN) combination. resolveUserCmdText gives us the
// human-readable description; isCountedStep tells us whether it's a
// user step. Empty descriptions (e.g. cleanup at stepN==0) are skipped.
func announceStep(reporter status.Reporter, phase string, stepN int, cfg schema.Config) {
	if reporter == nil {
		return
	}
	desc := resolveUserCmdText(phase, stepN, cfg)
	if desc == "" {
		return
	}
	reporter.Step(desc, isCountedStep(phase, stepN))
}

// isCountedStep reports whether step N of phase is a user-defined step
// (counted toward [K/Total]) or devm-internal (uncounted).
//
//	install:   step 1     = bootstrap (uncounted)
//	           step 2..N+1 = user (counted)
//	startup:   step 0 = cleanup (not displayed)
//	           step 1 = init-volumes (uncounted)
//	           step 2 = install-templates (uncounted)
//	           step 3..M+2 = user (counted)
func isCountedStep(phase string, stepN int) bool {
	if phase == "install" {
		return stepN >= 2
	}
	if phase == "startup" {
		return stepN >= 3
	}
	return false
}

// detectHighestOk lists /tmp/.devm-<phase>/ and returns the highest N
// for which <phase>-N.ok exists, or 0 if none. Best-effort: returns 0
// on any error (next poll cycle will try again).
func detectHighestOk(sb *sandbox.Sandbox, phase string) int {
	out, err := sb.Runner.Output("sbx", "exec", sb.Name, "ls", fmt.Sprintf("/tmp/.devm-%s/", phase))
	if err != nil {
		return 0
	}
	prefix := phase + "-"
	maxN := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, ".ok") {
			continue
		}
		body := strings.TrimSuffix(strings.TrimPrefix(line, prefix), ".ok")
		var n int
		if _, err := fmt.Sscanf(body, "%d", &n); err != nil {
			continue
		}
		if n > maxN {
			maxN = n
		}
	}
	return maxN
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

// readPhaseFailureFromHost is the host-side analog of readPhaseFailure,
// used when the anchor died (sbx torn down per c02) and the in-VM
// /tmp/.devm-<phase>/ is gone. wrap-fg.sh mirrors failure records to
// <repoRoot>/.devm/failures/<phase>-<N>.{current,rc} for exactly this
// case (pinned by c32-c34). Returns nil if no failure files found.
//
// Walks failure files for the given phase, identifies the LOWEST N
// (the first failing step), returns a FailureReport. Note: only files
// written by FAILING steps appear in .devm/failures/ — successful steps
// don't mirror, so any file in there is by definition a failure.
func readPhaseFailureFromHost(repoRoot, phase string, cfg schema.Config) (*FailureReport, error) {
	failuresDir := filepath.Join(repoRoot, ".devm", "failures")
	entries, err := os.ReadDir(failuresDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No failure mirrored — caller falls back.
		}
		return nil, fmt.Errorf("readPhaseFailureFromHost: read %s: %w", failuresDir, err)
	}

	prefix := phase + "-"
	rcN := -1
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".rc") {
			continue
		}
		// Parse <phase>-<N>.rc
		body := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".rc")
		var n int
		if _, err := fmt.Sscanf(body, "%d", &n); err != nil {
			continue
		}
		if rcN < 0 || n < rcN {
			rcN = n
		}
	}
	if rcN < 0 {
		return nil, nil
	}

	rcPath := filepath.Join(failuresDir, fmt.Sprintf("%s-%d.rc", phase, rcN))
	rcRaw, err := os.ReadFile(rcPath)
	if err != nil {
		return nil, fmt.Errorf("readPhaseFailureFromHost: read %s: %w", rcPath, err)
	}
	rc, perr := strconv.Atoi(strings.TrimSpace(string(rcRaw)))
	if perr != nil {
		return nil, fmt.Errorf("readPhaseFailureFromHost: parse rc %q: %w", rcRaw, perr)
	}

	report := &FailureReport{
		Phase:   phase,
		StepN:   rcN,
		RC:      rc,
		UserCmd: resolveUserCmdText(phase, rcN, cfg),
	}

	curPath := filepath.Join(failuresDir, fmt.Sprintf("%s-%d.current", phase, rcN))
	if curRaw, err := os.ReadFile(curPath); err == nil {
		full := string(curRaw)
		if len(full) > captureTailBytes {
			report.CapturedTail = full[len(full)-captureTailBytes:]
			report.Truncated = true
		} else {
			report.CapturedTail = full
		}
	}

	return report, nil
}
