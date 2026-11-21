package orchestrator

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeReporter is a minimal status.Reporter test double that records Step
// and Info calls in order (steps and infos are tracked in separate slices
// since the parser only exercises those two methods).
type fakeReporter struct {
	mu    sync.Mutex
	steps []string
	infos []string
}

func (f *fakeReporter) Start(msg string)   {}
func (f *fakeReporter) SetTotal(total int) {}
func (f *fakeReporter) Fail()              {}
func (f *fakeReporter) Stop()              {}
func (f *fakeReporter) Clear()             {}

func (f *fakeReporter) Step(desc string, counted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, desc)
}

func (f *fakeReporter) Info(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.infos = append(f.infos, msg)
}

func TestProvisionProgress_StagesAndCapture(t *testing.T) {
	r := &fakeReporter{}
	pp := newProvisionProgress(r)
	pp.Line("stdout", "::devm:stage:packages::")
	pp.Line("stdout", "installing jq...")
	pp.Line("stderr", "apt noise")
	pp.Line("stdout", "::devm:progress:install:2:3::")
	pp.Line("stdout", "::devm:stage:startup::")

	require.Equal(t, []string{"apt install packages", "run startup:"}, r.steps)
	require.Contains(t, r.infos, "install [2/3]")
	// non-marker lines are captured, markers are not
	require.Contains(t, pp.FailureOutput(), "apt noise")
	require.NotContains(t, pp.FailureOutput(), "::devm:stage")
}

// TestProvisionProgress_UnknownStageIsIgnored verifies that a stage name not
// present in stageLabels does not advance the spinner (and does not reset
// the capture buffer) — only recognized long-running stages get their own
// spinner label.
func TestProvisionProgress_UnknownStageIsIgnored(t *testing.T) {
	r := &fakeReporter{}
	pp := newProvisionProgress(r)
	pp.Line("stdout", "some output before")
	pp.Line("stdout", "::devm:stage:mystery::")

	require.Empty(t, r.steps)
	require.Contains(t, pp.FailureOutput(), "some output before")
}

// TestProvisionProgress_ConcurrentLines feeds Line from two goroutines
// concurrently (mirroring ExecStream's stdout/stderr goroutines) and
// verifies no data race in the shared capture buffer or reporter calls.
// Run with `go test -race`.
func TestProvisionProgress_ConcurrentLines(t *testing.T) {
	r := &fakeReporter{}
	pp := newProvisionProgress(r)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			pp.Line("stdout", "stdout line")
			pp.Line("stdout", "::devm:stage:packages::")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			pp.Line("stderr", "stderr line")
			pp.Line("stdout", "::devm:progress:install:1:2::")
		}
	}()
	wg.Wait()

	// No assertion on exact interleaving — just that nothing raced and
	// the parser stayed internally consistent (FailureOutput is readable).
	_ = pp.FailureOutput()
}
