package status

import (
	"fmt"
	"io"
	"os"
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

// Prefix text is the bare glyph — no leading space. pterm's
// PrefixPrinter wraps it with one space on each side ("  PREFIX  "),
// which puts the glyph at the same column as the spinnerSequence char
// (which itself has one leading space). With a leading space in the
// prefix text, pterm rendered `  ✓  ` (5 chars) while the spinner
// rendered ` ⠋ ` (3 chars) — labels visibly shifted between running
// and finalized rows.
var (
	successPrefix = pterm.PrefixPrinter{
		MessageStyle: pterm.NewStyle(pterm.FgDefault),
		Prefix: pterm.Prefix{
			Style: pterm.NewStyle(pterm.FgGreen),
			Text:  "✓",
		},
	}
	failPrefix = pterm.PrefixPrinter{
		MessageStyle: pterm.NewStyle(pterm.FgDefault),
		Prefix: pterm.Prefix{
			Style: pterm.NewStyle(pterm.FgRed),
			Text:  "✗",
		},
	}
)

// One leading + one trailing space per spinner frame so BOTH the
// glyph column AND the message column line up with what pterm's
// PrefixPrinter emits on finalized rows.
//
// Pterm internals (verified against pterm@v0.12.83):
//
//	PrefixPrinter.Sprint   → " " + text + " " + " " + message
//	                         = " ✓  message"   (glyph col 2, msg col 5)
//	SpinnerPrinter render  → seq + " " + message
//
// With seq " ⠋ ", spinner renders " ⠋ " + " " + message
//
//	= " ⠋  message"  (glyph col 2, msg col 5).
//
// Exact match: both glyph and message sit at the same column on every
// row, so the running ⠋ doesn't visually jump as it resolves to ✓/✗.
var spinnerSequence = []string{" ⠋ ", " ⠙ ", " ⠹ ", " ⠸ ", " ⠼ ", " ⠴ ", " ⠦ ", " ⠧ ", " ⠇ ", " ⠏ "}

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

// finalizeLocked resolves the active spinner as ✓ with elapsed time
// (only for steps that took >= 1s — sub-second times are noise).
func (r *PtermReporter) finalizeLocked() {
	if r.spinner == nil {
		return
	}
	elapsed := time.Since(r.curStart)
	final := r.curLabel
	if elapsed >= time.Second {
		final += " " + pterm.FgGray.Sprintf("(%s)", formatElapsed(elapsed))
	}
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
	final := r.curLabel
	if elapsed >= time.Second {
		final += " " + pterm.FgGray.Sprintf("(%s)", formatElapsed(elapsed))
	}
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
	// Pterm's spinner goroutine may have a queued frame after
	// Success() returns; the goroutine's tick interval is ~200ms,
	// so 50ms wasn't enough. Without this drain, anything writing
	// to the terminal immediately after Stop() (e.g., a sudo
	// password prompt) lands on top of a stale spinner frame.
	time.Sleep(250 * time.Millisecond)
}

// Clear emits ANSI escape codes to clear the visible terminal region
// and home the cursor. Scrollback is preserved (use \033[3J if you
// ever want to wipe scrollback too).
//
//	\033[H — cursor home
//	\033[2J — erase entire display (visible region)
//
// Writes to os.Stdout (where pterm renders the spinner) rather than
// os.Stderr — the kernel doesn't order writes across separate fds,
// so emitting on the same stream pterm uses lets Go's stdout mutex
// serialize Clear after any final spinner frame. A tiny drain sleep
// gives pterm's goroutine a chance to flush its last queued tick
// before we overwrite (without it, "⠋ ready (0s)" can land inline
// with the next process's output — e.g. the shell prompt after a
// PTY hand-off).
func (r *PtermReporter) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finalizeLocked()
	time.Sleep(50 * time.Millisecond)
	fmt.Fprint(os.Stdout, "\033[H\033[2J")
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
