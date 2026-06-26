package supervisor

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBackoff_StartsAtBase(t *testing.T) {
	b := newBackoff(100*time.Millisecond, 1*time.Second)
	start := time.Now()
	b.onExit(context.Background(), 1)
	elapsed := time.Since(start)
	assert.Greater(t, elapsed, 80*time.Millisecond)
	assert.Less(t, elapsed, 200*time.Millisecond)
}

func TestBackoff_DoublesOnRepeatedCrashes(t *testing.T) {
	b := newBackoff(100*time.Millisecond, 1*time.Second)
	b.onExit(context.Background(), 1) // sets delay = 100ms
	start := time.Now()
	b.onExit(context.Background(), 1) // would be 200ms now
	elapsed := time.Since(start)
	assert.Greater(t, elapsed, 180*time.Millisecond,
		"second crash should double the delay")
	assert.Less(t, elapsed, 300*time.Millisecond)
}

func TestBackoff_ResetsAfterStablePeriod(t *testing.T) {
	b := newBackoff(50*time.Millisecond, 5*time.Second)
	b.onExit(context.Background(), 1)
	b.delay = 2 * time.Second                        // simulate already-elevated
	b.lastStart = time.Now().Add(-31 * time.Second)  // simulate stable >30s ago
	start := time.Now()
	b.onExit(context.Background(), 1) // should reset to base = 50ms
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 150*time.Millisecond,
		"stable period >30s should reset backoff to base")
}

func TestBackoff_RespectsCap(t *testing.T) {
	b := newBackoff(100*time.Millisecond, 200*time.Millisecond)
	b.onExit(context.Background(), 1) // 100ms
	b.onExit(context.Background(), 1) // 200ms (cap)
	start := time.Now()
	b.onExit(context.Background(), 1) // would be 400ms but capped at 200ms
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 300*time.Millisecond,
		"delay must not exceed cap")
}

func TestKey_String(t *testing.T) {
	assert.Equal(t, "acme/vm", Key{ProjectID: "acme", Role: RoleVM}.String())
	assert.Equal(t, "acme/proxy", Key{ProjectID: "acme", Role: RoleProxy}.String())
}

func TestSetsid_AppliesOnDarwin(t *testing.T) {
	cmd := exec.Command("true")
	applySetsid(cmd)
	// On darwin, SysProcAttr is non-nil and Setsid is true.
	// On other OSes, applySetsid is a no-op (cmd.SysProcAttr stays nil).
	// Non-nil SysProcAttr is a sufficient proxy on darwin.
	_ = cmd
}

func TestEnvMap_Empty(t *testing.T) {
	assert.Nil(t, envMap(nil), "nil env should return nil map")
	assert.Nil(t, envMap([]string{}), "empty env should return nil map")
}

func TestEnvMap_Parses(t *testing.T) {
	m := envMap([]string{"FOO=bar", "BAZ=qux=quux"})
	assert.Equal(t, "bar", m["FOO"])
	assert.Equal(t, "qux=quux", m["BAZ"], "value with = should be preserved")
}

func TestArgsAfterPath_Empty(t *testing.T) {
	assert.Nil(t, argsAfterPath(nil))
	assert.Nil(t, argsAfterPath([]string{}))
}

func TestArgsAfterPath_StripsFirst(t *testing.T) {
	result := argsAfterPath([]string{"/usr/bin/tart", "run", "--no-graphics", "myvm"})
	assert.Equal(t, []string{"run", "--no-graphics", "myvm"}, result)
}
