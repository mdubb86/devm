package serviceapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/stretchr/testify/assert"
)

// fakeStopper is a vmStopper that records the poweroff exec and reports the
// VM as running until its List has been polled stopAfter times. If
// execFailFrom is > 0, Exec calls from that call number onward (1-indexed,
// counting the initial poweroff exec) return a non-zero ExitCode — standing
// in for the guest-agent going unreachable once the guest actually halts.
type fakeStopper struct {
	mu           sync.Mutex
	execName     string
	execArgv     []string
	execCalls    int
	execFailFrom int
	listCalls    int
	stopAfter    int
	name         string
}

func (f *fakeStopper) Exec(_ context.Context, name string, argv []string) tart.ExecResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execName = name
	f.execArgv = argv
	f.execCalls++
	if f.execFailFrom > 0 && f.execCalls >= f.execFailFrom {
		return tart.ExecResult{ExitCode: -1, Stderr: "connection refused: guest agent unreachable"}
	}
	return tart.ExecResult{}
}

func (f *fakeStopper) List(context.Context) ([]tart.VM, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	return []tart.VM{{Name: f.name, Running: f.listCalls < f.stopAfter}}, nil
}

func TestGracefulStopVM_PowersOffAndWaitsForStop(t *testing.T) {
	f := &fakeStopper{name: "proj", stopAfter: 1} // not-running on the first poll
	gracefulStopVM(context.Background(), f, "proj")

	assert.Equal(t, "proj", f.execName)
	assert.Equal(t, []string{"sudo", "systemctl", "poweroff"}, f.execArgv,
		"must ask the guest to power itself off cleanly")
	assert.GreaterOrEqual(t, f.listCalls, 1, "must poll for the VM to actually stop")
}

// If the guest never powers off, gracefulStopVM must still return when its
// grace window elapses — otherwise it would block the stop handler forever
// instead of falling through to the supervisor's force-terminate.
func TestGracefulStopVM_ReturnsOnTimeoutWhenGuestNeverStops(t *testing.T) {
	f := &fakeStopper{name: "proj", stopAfter: 1 << 30} // never reports stopped
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() { gracefulStopVM(ctx, f, "proj"); close(done) }()
	select {
	case <-done:
		// Returned without hanging — the handler proceeds to force-stop.
	case <-time.After(5 * time.Second):
		t.Fatal("gracefulStopVM did not return after its grace window; would block /vm/stop")
	}
}

// Under --net-softnet, tart list's Running flag never reflects the guest
// poweroff, so gracefulStopVM must detect the guest going down via
// guest-agent (Exec) reachability instead — and return promptly, not after
// the full 45s shutdownGraceTimeout. This models that: List always reports
// running (the softnet gap), but Exec starts failing once the guest halts.
func TestGracefulStopVM_DetectsStopViaExecUnreachable_UnderSoftnet(t *testing.T) {
	f := &fakeStopper{
		name:         "proj",
		stopAfter:    1 << 30, // tart list never reports stopped (the softnet gap)
		execFailFrom: 3,       // call 1 = poweroff; probes fail from call 3 onward
	}

	start := time.Now()
	// Generous ceiling well under the 45s cap — if this test needs to wait
	// anywhere near that long, the guest-agent-reachability detection isn't
	// working and gracefulStopVM fell back to spinning on List.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() { gracefulStopVM(ctx, f, "proj"); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("gracefulStopVM did not return once the guest agent became unreachable")
	}
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 5*time.Second,
		"must return promptly on guest-agent unreachability, not loop toward the 45s cap")
	assert.GreaterOrEqual(t, f.execCalls, 5,
		"must require 3 consecutive Exec failures (poweroff + at least 1 success + 3 failures)")
}
