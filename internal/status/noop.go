package status

// NoOpReporter discards all events. Used by tests that don't care
// about UX side-effects.
type NoOpReporter struct{}

func (NoOpReporter) Start(string)         {}
func (NoOpReporter) SetTotal(int)         {}
func (NoOpReporter) Step(string, bool)    {}
func (NoOpReporter) Fail()                {}
func (NoOpReporter) Info(string)          {}
func (NoOpReporter) Stop()                {}
