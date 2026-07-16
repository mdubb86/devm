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
// VM as running until its List has been polled stopAfter times.
type fakeStopper struct {
	mu        sync.Mutex
	execName  string
	execArgv  []string
	listCalls int
	stopAfter int
	name      string
}

func (f *fakeStopper) Exec(_ context.Context, name string, argv []string) tart.ExecResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execName = name
	f.execArgv = argv
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
