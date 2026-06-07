package status

import "time"

// NoOpReporter discards all events. Used by tests that don't care
// about UX side-effects.
type NoOpReporter struct{}

func (NoOpReporter) Start(string)                    {}
func (NoOpReporter) Info(string)                     {}
func (NoOpReporter) PhaseStart(string, int)          {}
func (NoOpReporter) StepStart(string, int, string)   {}
func (NoOpReporter) StepDone(string, int, time.Duration) {}
func (NoOpReporter) StepFail(string, int, time.Duration) {}
func (NoOpReporter) PhaseDone(string, time.Duration) {}
func (NoOpReporter) Stop()                           {}
