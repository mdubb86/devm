package serviceapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mdubb86/devm/internal/identity"
)

func TestParseOrphanSoftnets(t *testing.T) {
	binary := "/Users/michael/Library/Application Support/devm/softnet-bin/softnet"
	psOutput := `  100     1 /usr/sbin/syslogd
  200     1 /Users/michael/Library/Application Support/devm/softnet-bin/softnet --vm-fd 0 --vm-mac-address aa:bb:cc:dd:ee:01
  300 45000 /Users/michael/Library/Application Support/devm/softnet-bin/softnet --vm-fd 0 --vm-mac-address aa:bb:cc:dd:ee:02
  400     1 /opt/homebrew/bin/softnet --vm-fd 0 --vm-mac-address aa:bb:cc:dd:ee:03
  500     1 /Users/michael/Library/Application Support/devm/softnet-bin/softnet --version
notanint  1 /Users/michael/Library/Application Support/devm/softnet-bin/softnet --vm-fd 0
`
	got := parseOrphanSoftnets(psOutput, binary)

	assert.Contains(t, got, 200, "PPID==1 + matching binary must be reaped")
	assert.NotContains(t, got, 300, "a softnet with a live (non-1) PPID — still owned by a running tart-run — must never be touched")
	assert.NotContains(t, got, 400, "a different binary path must not be adopted even with PPID==1")
	assert.NotContains(t, got, 100, "an unrelated process must be ignored")
	assert.Len(t, got, 2, "expected exactly the two matching orphaned softnet PIDs (200 and 500)")
}

func TestParseOrphanSoftnets_EmptyInput(t *testing.T) {
	assert.Empty(t, parseOrphanSoftnets("", "/anywhere/softnet-bin/softnet"))
}

func TestParseOrphanSoftnets_NoMatches(t *testing.T) {
	psOutput := `  100     1 /usr/sbin/syslogd
  200 45000 /Users/michael/Library/Application Support/devm/softnet-bin/softnet --vm-fd 0
`
	assert.Empty(t, parseOrphanSoftnets(psOutput, "/Users/michael/Library/Application Support/devm/softnet-bin/softnet"))
}

// TestReapOrphanSoftnets_RunsInBackgroundWithoutBlocking covers the
// daemon-startup call site: on a normal dev machine with no orphaned
// softnets present, ReapOrphanSoftnets must return immediately (it shells
// out to `ps` and does its work in a background goroutine) rather than
// stalling RunService's startup path.
func TestReapOrphanSoftnets_RunsInBackgroundWithoutBlocking(t *testing.T) {
	done := make(chan struct{})
	go func() {
		ReapOrphanSoftnets(context.Background(), identity.Prod)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ReapOrphanSoftnets must return immediately, not block on ps/kill")
	}
}
