// Package status provides a cold-start UX reporter for `devm shell`.
//
// Two implementations live alongside this file:
//   - PtermReporter (pterm.go) — single-line spinner, ANSI color, ✓/✗ on done
//   - PlainReporter (this file) — plain transcript, no ANSI
//
// New(out) picks the right implementation based on whether out is a TTY.
package status

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// Reporter receives step events during cold start.
//
// All calls are pure side-effects on the underlying output. Errors are
// swallowed — the reporter is UX, not load-bearing.
//
// Lifecycle:
//
//   - Start(msg) — instant spinner the moment `devm shell` runs.
//   - SetTotal(N) — once we know how many counted (user-visible) steps to expect.
//   - Step(desc, counted) — begin a new step. Implicitly finalizes the
//     previous step as ✓. counted=true → display prefixes with "[K/N] ".
//     counted=false → devm-internal step, no prefix, doesn't increment K.
//   - Fail() — finalize the current step as ✗. Only needed on failure;
//     successful completion is implied by the next Step or Stop.
//   - Info(msg) — gray informational line outside the step flow.
//   - Stop() — finalize the trailing spinner as ✓; idempotent.
type Reporter interface {
	Start(msg string)
	SetTotal(total int)
	Step(desc string, counted bool)
	Fail()
	Info(msg string)
	Stop()

	// Clear wipes the visible terminal region so the user's shell
	// prompt drops in on a clean screen. Scrollback is preserved.
	// Called by the caller on the SUCCESS path right before PTY
	// hand-off; NOT on failure (the error block must stay visible).
	// PlainReporter implements Clear as a no-op.
	Clear()
}

// New returns a Reporter appropriate for out. If out is a TTY, a
// PtermReporter is returned; otherwise a PlainReporter. NO_COLOR=1
// forces PlainReporter even on a TTY.
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
type PlainReporter struct {
	Out io.Writer

	total int
	count int
}

func (r *PlainReporter) Start(msg string) {
	fmt.Fprintf(r.Out, "[devm] %s\n", msg)
}

func (r *PlainReporter) SetTotal(total int) {
	r.total = total
}

func (r *PlainReporter) Step(desc string, counted bool) {
	if counted {
		r.count++
		fmt.Fprintf(r.Out, "[devm] [%d/%d] %s\n", r.count, r.total, desc)
		return
	}
	fmt.Fprintf(r.Out, "[devm] %s\n", desc)
}

func (r *PlainReporter) Fail() {
	fmt.Fprintf(r.Out, "[devm] FAILED\n")
}

func (r *PlainReporter) Info(msg string) {
	fmt.Fprintf(r.Out, "[devm] %s\n", msg)
}

func (r *PlainReporter) Stop()  {}
func (r *PlainReporter) Clear() {} // no-op: plain transcripts shouldn't be wiped
