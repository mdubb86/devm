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
	curLabel string    // label of the active spinner (for finalize)
	curStart time.Time // when the active spinner began
	total    int       // total counted steps (set via SetTotal)
	count    int       // index of the most recent counted step
}

func newPtermReporter(out io.Writer) *PtermReporter {
	return &PtermReporter{out: out}
}

var (
	successPrefix = pterm.PrefixPrinter{
		MessageStyle: pterm.NewStyle(pterm.FgDefault),
		Prefix: pterm.Prefix{
			Style: pterm.NewStyle(pterm.FgGreen),
			Text:  " ✓",
		},
	}
	failPrefix = pterm.PrefixPrinter{
		MessageStyle: pterm.NewStyle(pterm.FgDefault),
		Prefix: pterm.Prefix{
			Style: pterm.NewStyle(pterm.FgRed),
			Text:  " ✗",
		},
	}
)

var spinnerSequence = []string{" ⠋", " ⠙", " ⠹", " ⠸", " ⠼", " ⠴", " ⠦", " ⠧", " ⠇", " ⠏"}

func newSpinner(text string) *pterm.SpinnerPrinter {
	sp, _ := pterm.DefaultSpinner.
		WithRemoveWhenDone(false).
		WithShowTimer(true).
		WithSequence(spinnerSequence...).
		WithStyle(pterm.NewStyle(pterm.FgCyan)).
		WithText(text).
		Start()
	sp.SuccessPrinter = &successPrefix
	sp.FailPrinter = &failPrefix
	return sp
}

// finalizeLocked resolves the active spinner as ✓ with elapsed time.
func (r *PtermReporter) finalizeLocked() {
	if r.spinner == nil {
		return
	}
	elapsed := time.Since(r.curStart)
	final := r.curLabel + " " + pterm.FgGray.Sprintf("(%s)", formatElapsed(elapsed))
	r.spinner.Success(final)
	r.spinner = nil
}

// startSpinnerLocked finalizes any active spinner with ✓ and starts a new one.
func (r *PtermReporter) startSpinnerLocked(label string) {
	r.finalizeLocked()
	r.curLabel = label
	r.curStart = time.Now()
	r.spinner = newSpinner(label)
}

func (r *PtermReporter) Start(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spinner != nil {
		// Already running — just update text. Timer keeps ticking.
		r.curLabel = msg
		r.spinner.UpdateText(msg)
		return
	}
	r.startSpinnerLocked(msg)
}

func (r *PtermReporter) SetTotal(total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total = total
}

func (r *PtermReporter) Step(desc string, counted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var label string
	if counted {
		r.count++
		label = fmt.Sprintf("[%d/%d] %s", r.count, r.total, desc)
	} else {
		label = desc
	}
	r.startSpinnerLocked(label)
}

func (r *PtermReporter) Fail() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spinner == nil {
		return
	}
	elapsed := time.Since(r.curStart)
	final := r.curLabel + " " + pterm.FgGray.Sprintf("(%s)", formatElapsed(elapsed))
	r.spinner.Fail(final)
	r.spinner = nil
}

func (r *PtermReporter) Info(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finalizeLocked()
	pterm.FgGray.Println("[devm] " + msg)
}

func (r *PtermReporter) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finalizeLocked()
}

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
