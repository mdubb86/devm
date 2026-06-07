// Package status provides a cold-start UX reporter for `devm shell`.
//
// The Reporter interface is the seam between the orchestrator (which
// drives phase + step state machine) and the visual layer (which can
// be a fancy spinner on a TTY or a plain transcript everywhere else).
//
// Two implementations live alongside this file:
//   - PtermReporter (pterm.go) — single-line spinner, ANSI color, ✓/✗ on done
//   - PlainReporter (this file) — plain transcript, no ANSI
//
// A NoOpReporter (noop.go) is provided for tests that don't care about
// the visual side-effects.
//
// New(out) picks the right implementation based on whether out is a TTY.
package status

import (
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/term"
)

// Reporter receives phase/step events during cold start.
//
// All calls are pure side-effects on the underlying output. Errors are
// swallowed — the reporter is UX, not load-bearing.
type Reporter interface {
	// Start shows a generic "indeterminate" spinner with the given
	// initial message. Used the instant `devm shell` is invoked, before
	// we know cold vs warm, so the user sees something moving. The
	// first PhaseStart/StepStart/Info call transitions out of this state.
	// Idempotent — calling Start twice is harmless (text updates).
	Start(msg string)

	// Info emits a one-off line outside the step machinery (e.g.
	// "starting sandbox", "ready").
	Info(msg string)

	// PhaseStart signals entering a phase.
	//   phase: "install" or "startup"
	//   total: user-facing step count for the "X / Y" display
	PhaseStart(phase string, total int)

	// StepStart signals beginning of step N (1-indexed for display).
	// desc is a short human-readable description.
	StepStart(phase string, n int, desc string)

	// StepDone signals successful completion of the current step.
	StepDone(phase string, n int, elapsed time.Duration)

	// StepFail signals failure of the current step.
	StepFail(phase string, n int, elapsed time.Duration)

	// PhaseDone signals successful completion of the phase.
	PhaseDone(phase string, elapsed time.Duration)

	// Stop tears down any active display. Idempotent.
	Stop()
}

// New returns a Reporter appropriate for out. If out is a TTY, a
// PtermReporter is returned; otherwise a PlainReporter. NO_COLOR=1
// forces PlainReporter even on a TTY (de facto standard).
func New(out io.Writer) Reporter {
	if os.Getenv("NO_COLOR") != "" {
		return &PlainReporter{Out: out}
	}
	if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return newPtermReporter(out)
	}
	return &PlainReporter{Out: out}
}

// PlainReporter emits a plain-text transcript. No ANSI. One line per event.
// Used when stderr isn't a TTY (CI, piped output) or when NO_COLOR is set.
type PlainReporter struct {
	Out io.Writer
}

func (r *PlainReporter) Start(msg string) {
	fmt.Fprintf(r.Out, "[devm] %s\n", msg)
}

func (r *PlainReporter) Info(msg string) {
	fmt.Fprintf(r.Out, "[devm] %s\n", msg)
}

func (r *PlainReporter) PhaseStart(phase string, total int) {
	fmt.Fprintf(r.Out, "[devm] %s: %d steps\n", phase, total)
}

func (r *PlainReporter) StepStart(phase string, n int, desc string) {
	fmt.Fprintf(r.Out, "[devm] %s [%d] %s: starting\n", phase, n, desc)
}

func (r *PlainReporter) StepDone(phase string, n int, elapsed time.Duration) {
	fmt.Fprintf(r.Out, "[devm] %s [%d]: done (%s)\n", phase, n, formatElapsed(elapsed))
}

func (r *PlainReporter) StepFail(phase string, n int, elapsed time.Duration) {
	fmt.Fprintf(r.Out, "[devm] %s [%d]: FAILED (%s)\n", phase, n, formatElapsed(elapsed))
}

func (r *PlainReporter) PhaseDone(phase string, elapsed time.Duration) {
	fmt.Fprintf(r.Out, "[devm] %s: done (%s)\n", phase, formatElapsed(elapsed))
}

func (r *PlainReporter) Stop() {}

// formatElapsed renders a duration in the format we want for the UX:
//
//	< 60s  → "12.3s"
//	>= 60s → "1m23s"
func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", m, s)
}
