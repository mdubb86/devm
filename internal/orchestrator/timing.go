package orchestrator

import (
	"fmt"
	"os"
	"time"
)

// timer emits phase timings to stderr when DEVM_TIMING is set in the
// environment. Off by default so normal runs stay quiet. Used to
// diagnose where cold-start / reconcile wall-clock time goes.
type timer struct {
	enabled bool
	label   string
	start   time.Time
	last    time.Time
}

func newTimer(label string) *timer {
	now := time.Now()
	return &timer{
		enabled: os.Getenv("DEVM_TIMING") != "",
		label:   label,
		start:   now,
		last:    now,
	}
}

// mark logs the elapsed time since the previous mark (and total since
// start) for the named phase.
func (t *timer) mark(phase string) {
	if t == nil || !t.enabled {
		return
	}
	now := time.Now()
	fmt.Fprintf(os.Stderr, "[devm-timing] %s: %-22s +%.1fs (total %.1fs)\n",
		t.label, phase, now.Sub(t.last).Seconds(), now.Sub(t.start).Seconds())
	t.last = now
}
