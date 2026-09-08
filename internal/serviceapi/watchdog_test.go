package serviceapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type recordingCheck struct {
	name     string
	drifted  bool
	err      error
	runCount int
}

func (c *recordingCheck) Name() string { return c.name }

func (c *recordingCheck) Run(ctx context.Context, cache *StateCache, gt GroundTruth) (bool, error) {
	c.runCount++
	return c.drifted, c.err
}

func TestStateWatchdog_RunOnce_CallsEveryCheck(t *testing.T) {
	checks := []Check{
		&recordingCheck{name: "a"},
		&recordingCheck{name: "b", drifted: true},
		&recordingCheck{name: "c"},
	}

	w := NewStateWatchdog(NewStateCache(), nil, checks, 60*time.Second)
	drifts := w.RunOnce(context.Background())

	assert.Equal(t, 1, drifts)
	for _, c := range checks {
		rc := c.(*recordingCheck)
		assert.Equal(t, 1, rc.runCount, "check %s should run exactly once", rc.name)
	}
}

func TestStateWatchdog_Run_FiresRunOnce_ThenCancels(t *testing.T) {
	check := &recordingCheck{name: "ticking"}
	w := NewStateWatchdog(NewStateCache(), nil, []Check{check}, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	assert.Greater(t, check.runCount, 1, "ticker should have fired RunOnce more than once")
}

func TestStateWatchdog_RunOnce_CheckError_LoggedNotFatal(t *testing.T) {
	checks := []Check{
		&recordingCheck{name: "erroring", err: errors.New("boom")},
		&recordingCheck{name: "drifted", drifted: true},
	}

	w := NewStateWatchdog(NewStateCache(), nil, checks, 60*time.Second)
	drifts := w.RunOnce(context.Background())

	assert.Equal(t, 1, drifts)
}
