package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAptRetryHelper_ThreeAttemptsWithBackoff(t *testing.T) {
	s := AptRetryHelper()
	// The apt call gets both fixes at once: apt's own per-file retry
	// (Acquire::Retries=3) AND the outer whole-batch retry loop the
	// helper wraps around it.
	assert.Contains(t, s, "sudo apt-get -o Acquire::Retries=3 -o DPkg::Lock::Timeout=60")
	// Three total attempts.
	assert.Contains(t, s, "for attempt in 1 2 3")
	// Progressive backoff: attempt N waits N*5 seconds — 5s, 15s.
	assert.Contains(t, s, "attempt * 5")
	// Fail-loud message on final give-up.
	assert.Contains(t, s, "failed after 3 attempts")
}

func TestAptRetryHelper_DefinesAptRunFunction(t *testing.T) {
	s := AptRetryHelper()
	assert.True(t, strings.HasPrefix(s, "apt_run() {"), "helper must define apt_run at top:\n%s", s)
}

func TestProvisionUser_EmitsAptRunHelper(t *testing.T) {
	// The provisioning script MUST define apt_run before either the
	// firstboot packages block or the warm-reconcile converge block
	// calls it. Emitted unconditionally so both entrypoints are safe.
	s := string(RenderProvisionUserScript(ProvisionScriptInput{
		FirstBoot: true,
		Packages:  []string{"jq"},
	}))
	assert.Contains(t, s, "apt_run() {")
	// apt_run definition precedes its call.
	defIdx := strings.Index(s, "apt_run() {")
	callIdx := strings.Index(s, "apt_run install -y")
	assert.Greater(t, callIdx, defIdx, "apt_run must be defined before it is called:\n%s", s)
}
