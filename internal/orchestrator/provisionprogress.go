package orchestrator

import (
	"strings"
	"sync"

	"github.com/mdubb86/devm/internal/status"
)

// stageLabels maps a script stage marker name to its spinner label. Only the
// long-running stages get their own line; everything else stays under the
// caller's single "provisioning" spinner (Task 6 starts that).
var stageLabels = map[string]string{
	"open":     "opening network",
	"packages": "apt install packages",
	"install":  "run install:",
	"docker":   "docker feature",
	"startup":  "run startup:",
}

// provisionProgress parses the provisioning script's stage markers
// (`::devm:stage:<name>::`, `::devm:progress:<name>:<i>:<n>::`) from
// ExecStream's line-by-line output, driving a status.Reporter spinner and
// capturing non-marker output for a failure dump.
type provisionProgress struct {
	r status.Reporter

	mu      sync.Mutex
	capture strings.Builder // non-marker output of the run so far, for the failure dump
}

func newProvisionProgress(r status.Reporter) *provisionProgress {
	return &provisionProgress{r: r}
}

// Line consumes one output line from ExecStream. Stage markers advance the
// spinner; progress markers emit sub-progress; everything else is captured
// for the failure dump.
//
// ExecStream invokes onLine from two concurrent goroutines (one per
// stream) with no serialization between them, so Line locks its own state.
func (pp *provisionProgress) Line(stream, line string) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	switch {
	case strings.HasPrefix(line, "::devm:stage:"):
		name := strings.TrimSuffix(strings.TrimPrefix(line, "::devm:stage:"), "::")
		if label, ok := stageLabels[name]; ok {
			pp.r.Step(label, false)
		}
	case strings.HasPrefix(line, "::devm:progress:"):
		// ::devm:progress:install:2:3::
		body := strings.TrimSuffix(strings.TrimPrefix(line, "::devm:progress:"), "::")
		parts := strings.Split(body, ":")
		if len(parts) == 3 {
			pp.r.Info(parts[0] + " [" + parts[1] + "/" + parts[2] + "]")
		}
	default:
		pp.capture.WriteString(line)
		pp.capture.WriteByte('\n')
	}
}

// FailureOutput returns the captured non-marker output of the run so far,
// dumped when provisioning fails.
func (pp *provisionProgress) FailureOutput() string {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return pp.capture.String()
}
