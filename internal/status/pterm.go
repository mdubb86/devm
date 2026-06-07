package status

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/pterm/pterm"
)

// PtermReporter renders a single-line spinner that replaces in place,
// with ✓/✗ checkmarks on step completion. Uses pterm under the hood.
type PtermReporter struct {
	out io.Writer

	mu       sync.Mutex
	spinner  *pterm.SpinnerPrinter
	curPhase string
	curN     int
	curTotal int
	curDesc  string
}

func newPtermReporter(out io.Writer) *PtermReporter {
	// pterm's default writers are stdout/stderr-bound. We let pterm
	// drive the TTY natively rather than redirecting via Out — the
	// terminal control chars need a real fd to land correctly.
	return &PtermReporter{out: out}
}

func (r *PtermReporter) Start(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spinner != nil {
		// Already running — just update text.
		r.spinner.UpdateText(msg)
		return
	}
	sp, _ := pterm.DefaultSpinner.
		WithRemoveWhenDone(false).
		WithShowTimer(true).
		WithText(msg).
		Start()
	r.spinner = sp
}

func (r *PtermReporter) Info(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopSpinnerLocked()
	pterm.FgGray.Println("[devm] " + msg)
}

func (r *PtermReporter) PhaseStart(phase string, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.curPhase = phase
	r.curTotal = total
	r.curN = 0
}

func (r *PtermReporter) StepStart(phase string, n int, desc string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopSpinnerLocked()
	r.curN = n
	r.curDesc = desc
	label := stepLabel(phase, n, r.curTotal, desc)
	sp, _ := pterm.DefaultSpinner.
		WithRemoveWhenDone(false).
		WithShowTimer(true).
		WithText(label).
		Start()
	r.spinner = sp
}

// stepLabel renders the line head as:
//
//	"phase [N/M] desc"  when phase != "" and total > 0
//	"phase desc"        when phase != "" but no count (rare)
//	"desc"              when phase == "" (label-only step)
func stepLabel(phase string, n, total int, desc string) string {
	if phase == "" {
		return desc
	}
	if total > 0 {
		return fmt.Sprintf("%s [%d/%d] %s", phase, n, total, desc)
	}
	return fmt.Sprintf("%s %s", phase, desc)
}

func (r *PtermReporter) StepDone(phase string, n int, elapsed time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	final := stepLabel(phase, n, r.curTotal, r.curDesc) + " " +
		pterm.FgGray.Sprintf("(%s)", formatElapsed(elapsed))
	if r.spinner != nil {
		r.spinner.Success(final)
		r.spinner = nil
	} else {
		pterm.Success.Println(final)
	}
}

func (r *PtermReporter) StepFail(phase string, n int, elapsed time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	final := stepLabel(phase, n, r.curTotal, r.curDesc) + " " +
		pterm.FgGray.Sprintf("(%s)", formatElapsed(elapsed))
	if r.spinner != nil {
		r.spinner.Fail(final)
		r.spinner = nil
	} else {
		pterm.Error.Println(final)
	}
}

func (r *PtermReporter) PhaseDone(phase string, elapsed time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Phase-level done line is intentionally subtle — the per-step ✓ lines
	// already conveyed completion. We skip a noisy "phase done" line.
	_ = phase
	_ = elapsed
}

func (r *PtermReporter) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopSpinnerLocked()
}

func (r *PtermReporter) stopSpinnerLocked() {
	if r.spinner == nil {
		return
	}
	// Spinner was active and we're stopping without success/fail —
	// treat as cancellation; leave a neutral marker.
	_ = r.spinner.Stop()
	r.spinner = nil
}
