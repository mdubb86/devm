package softnet

import (
	"context"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/identity"
)

func TestRunRejectsMissingFD(t *testing.T) {
	// No --vm-fd and no real socket: Run must return an error, not panic.
	err := Run(identity.Prod, []string{"--vm-mac-address", "aa:bb:cc:dd:ee:ff"})
	if err == nil {
		t.Fatal("Run without a valid --vm-fd should error")
	}
}

// TestAcceptUntilShutdown_ReturnsPromptlyOnCancelWithNoTraffic is the
// regression test for the orphan-softnet leak: sw.Accept's read loop only
// checks ctx.Done() between reads (see acceptUntilShutdown's doc comment),
// so on a vm-fd conn that's gone quiet — exactly softnet's state right
// after the guest powers off and teardown asks it to shut down — a plain
// ctx cancellation would leave Accept blocked in conn.Read forever. Without
// acceptUntilShutdown's force-close, this test would hang until its
// timeout instead of observing a prompt return.
func TestAcceptUntilShutdown_ReturnsPromptlyOnCancelWithNoTraffic(t *testing.T) {
	guest, softnetConn := socketpairConns(t)
	defer guest.Close()

	n, err := newNetwork()
	if err != nil {
		t.Fatalf("newNetwork: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- acceptUntilShutdown(ctx, n.sw, softnetConn) }()

	// Give the Accept goroutine time to pass its ctx.Done() check (only
	// checked *before* each Read — see acceptUntilShutdown's doc comment)
	// and actually park inside a blocking conn.Read. No traffic flows on
	// this socketpair at all, so without that Read genuinely blocked
	// first, cancelling immediately would trivially pass by catching the
	// pre-Read check instead of exercising the bug this test guards.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("acceptUntilShutdown returned an error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acceptUntilShutdown did not return after ctx cancellation with no traffic — " +
			"this is the orphan-softnet bug: the process would never exit")
	}
}
