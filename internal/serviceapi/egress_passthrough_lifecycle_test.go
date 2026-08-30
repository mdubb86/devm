package serviceapi

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/supervisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEgressPassthroughStore_PutGetRoundTrip(t *testing.T) {
	s := newEgressPassthroughStore()
	deadline := time.Now().Add(30 * time.Second)
	s.put("p1", deadline)

	entry, ok := s.get("p1")
	require.True(t, ok, "get after put must return the entry")
	assert.Equal(t, deadline, entry.expiresAt)
	assert.Nil(t, entry.restore, "put alone must not install a timer")
}

func TestEgressPassthroughStore_GetMissing_ReturnsFalse(t *testing.T) {
	s := newEgressPassthroughStore()
	_, ok := s.get("nope")
	assert.False(t, ok)
}

func TestEgressPassthroughStore_SetTimer_ReplacesPrevious(t *testing.T) {
	s := newEgressPassthroughStore()
	s.put("p1", time.Now().Add(time.Hour))

	var firedFirst atomic.Int32
	t1 := time.AfterFunc(10*time.Millisecond, func() { firedFirst.Add(1) })
	s.setTimer("p1", t1)

	// Replace before it can fire; the first timer must not fire after
	// the second is installed.
	var firedSecond atomic.Int32
	t2 := time.AfterFunc(50*time.Millisecond, func() { firedSecond.Add(1) })
	s.setTimer("p1", t2)

	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 0, firedFirst.Load(), "replaced timer must be stopped, not fire")
	assert.EqualValues(t, 1, firedSecond.Load(), "replacement timer must fire")
}

func TestEgressPassthroughStore_StopTimer_Cancels(t *testing.T) {
	s := newEgressPassthroughStore()
	s.put("p1", time.Now().Add(time.Hour))

	var fired atomic.Int32
	t1 := time.AfterFunc(50*time.Millisecond, func() { fired.Add(1) })
	s.setTimer("p1", t1)

	s.stopTimer("p1")
	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 0, fired.Load(), "stopTimer must cancel the pending fire")

	entry, ok := s.get("p1")
	require.True(t, ok, "stopTimer must not delete the entry")
	assert.Nil(t, entry.restore, "stopTimer must clear the timer field")
}

func TestEgressPassthroughStore_Del_StopsTimerAndRemovesEntry(t *testing.T) {
	s := newEgressPassthroughStore()
	s.put("p1", time.Now().Add(time.Hour))

	var fired atomic.Int32
	t1 := time.AfterFunc(50*time.Millisecond, func() { fired.Add(1) })
	s.setTimer("p1", t1)

	s.del("p1")

	_, ok := s.get("p1")
	assert.False(t, ok, "del must remove the entry")

	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 0, fired.Load(), "del must also cancel the pending timer")
}

func TestEgressPassthroughStore_DefaultDurationConst(t *testing.T) {
	// Pin the spec's default: `devm passthrough` (no --for) opens a
	// 30-second window. Longer defaults raise the security exposure;
	// shorter ones make the user re-invoke mid-supervision.
	assert.Equal(t, 30, defaultPassthroughSeconds)
}

// newFakeSoftnet stands up a temporary unix socket that captures the
// last setPolicy JSON message sent to it. Returned sockPath is
// registered with SetSoftnetControlSockForTest so /vm/* handlers find
// it. cleanup closes the listener and clears state.
func newFakeSoftnet(t *testing.T, projectID string) (sockPath string, last func() string, cleanup func()) {
	t.Helper()
	// os.MkdirTemp("/tmp", ...) rather than t.TempDir(): t.TempDir()
	// embeds the (long) test name in the path, which overflows unix
	// sun_path's ~104-byte limit on macOS and fails the Listen below.
	dir, err := os.MkdirTemp("/tmp", "softnet-fake-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath = filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	var mu sync.Mutex
	var lastMsg string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf, _ := io.ReadAll(conn)
			mu.Lock()
			lastMsg = string(buf)
			mu.Unlock()
			conn.Close()
		}
	}()
	SetSoftnetControlSockForTest(projectID, sockPath)
	last = func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastMsg
	}
	cleanup = func() {
		_ = ln.Close()
		softnetState.del(projectID)
	}
	return
}

func TestPassthroughEgress_FlipsAuthorityMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	const name = "passthrough-flip"
	ironProxyState.put(name, projectInfo{HTTPPort: 1, HTTPSPort: 2, DNSPort: 3})
	t.Cleanup(func() {
		ironProxyState.del(name)
		egressPassthroughState.del(name)
		policyAuthority.SetMode(name, ModeRestricted)
	})

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	wasOpen, expires, err := c.PassthroughEgress(ctx, name, 60)
	require.NoError(t, err)
	assert.False(t, wasOpen, "fresh open must report was_open=false")
	assert.Equal(t, 60, expires)

	assert.Equal(t, ModePassthrough, policyAuthority.modeFor(name), "passthrough must flip the authority mode")

	entry, ok := egressPassthroughState.get(name)
	require.True(t, ok, "state entry must exist after open")
	assert.WithinDuration(t, time.Now().Add(60*time.Second), entry.expiresAt, 2*time.Second)
}

func TestPassthroughEgress_ZeroDurationUsesDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	const name = "passthrough-default-dur"
	ironProxyState.put(name, projectInfo{HTTPPort: 1, HTTPSPort: 2, DNSPort: 3})
	t.Cleanup(func() {
		ironProxyState.del(name)
		egressPassthroughState.del(name)
	})
	_, _, cleanupSock := newFakeSoftnet(t, name)
	defer cleanupSock()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, expires, err := c.PassthroughEgress(ctx, name, 0)
	require.NoError(t, err)
	assert.Equal(t, defaultPassthroughSeconds, expires, "duration <= 0 must fall back to defaultPassthroughSeconds")
}

func TestPassthroughEgress_ReplacesInFlightTimer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	const name = "passthrough-replace-timer"
	ironProxyState.put(name, projectInfo{HTTPPort: 1, HTTPSPort: 2, DNSPort: 3})
	t.Cleanup(func() {
		ironProxyState.del(name)
		egressPassthroughState.del(name)
	})
	_, _, cleanupSock := newFakeSoftnet(t, name)
	defer cleanupSock()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// First open — very long.
	_, _, err := c.PassthroughEgress(ctx, name, 3600)
	require.NoError(t, err)
	first, _ := egressPassthroughState.get(name)

	// Second open — very short.
	_, _, err = c.PassthroughEgress(ctx, name, 1)
	require.NoError(t, err)
	second, _ := egressPassthroughState.get(name)

	assert.True(t, second.expiresAt.Before(first.expiresAt), "second open must shorten expiresAt")
}

func TestRestrictEgress_ClearsStateAndFlipsAuthorityMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	const name = "restrict-clears"
	ironProxyState.put(name, projectInfo{HTTPPort: 1, HTTPSPort: 2, DNSPort: 3})
	t.Cleanup(func() {
		ironProxyState.del(name)
		egressPassthroughState.del(name)
		policyAuthority.SetMode(name, ModeRestricted)
	})

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Open first.
	_, _, err := c.PassthroughEgress(ctx, name, 3600)
	require.NoError(t, err)
	require.Equal(t, ModePassthrough, policyAuthority.modeFor(name), "test setup: window open")

	// Restrict.
	wasOpen, err := c.RestrictEgress(ctx, name)
	require.NoError(t, err)
	assert.True(t, wasOpen)

	// State cleared.
	_, ok := egressPassthroughState.get(name)
	assert.False(t, ok, "restrict must del the state entry")

	assert.Equal(t, ModeRestricted, policyAuthority.modeFor(name), "restrict must flip the authority mode back")
}

func TestRestrictEgress_UnknownProject_NoOpNoError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	wasOpen, err := c.RestrictEgress(ctx, "no-such-project")
	require.NoError(t, err)
	assert.False(t, wasOpen, "restrict of an unknown project returns 200 was_open=false")
}

func TestPassthroughEgress_TimerFiresRestore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	const name = "passthrough-timer"
	ironProxyState.put(name, projectInfo{HTTPPort: 1, HTTPSPort: 2, DNSPort: 3})
	t.Cleanup(func() {
		ironProxyState.del(name)
		egressPassthroughState.del(name)
		policyAuthority.SetMode(name, ModeRestricted)
	})

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Open for 1 second.
	_, _, err := c.PassthroughEgress(ctx, name, 1)
	require.NoError(t, err)
	require.Equal(t, ModePassthrough, policyAuthority.modeFor(name), "test setup: window open")

	// Wait past deadline + a small margin for the timer to fire.
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) && policyAuthority.modeFor(name) != ModeRestricted {
		time.Sleep(50 * time.Millisecond)
	}
	assert.Equal(t, ModeRestricted, policyAuthority.modeFor(name), "timer must restore the authority mode to restricted")

	_, ok := egressPassthroughState.get(name)
	assert.False(t, ok, "timer-driven restore must clear state (same code path as restrict)")
}

func TestVMStop_ClearsPassthroughState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	script := "#!/bin/sh\ncase \"$1\" in\n  list) echo '[]' ;;\nesac\nexit 0\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	const name = "stop-clears-passthrough"
	ironProxyState.put(name, projectInfo{HTTPPort: 1, HTTPSPort: 2, DNSPort: 3})
	t.Cleanup(func() {
		ironProxyState.del(name)
		egressPassthroughState.del(name)
	})
	_, _, cleanupSock := newFakeSoftnet(t, name)
	defer cleanupSock()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Open a long window so the timer would otherwise still be armed.
	_, _, err := c.PassthroughEgress(ctx, name, 3600)
	require.NoError(t, err)
	require.NotNil(t, mustEntry(t, egressPassthroughState, name).restore, "test setup: timer armed")

	// /vm/stop must clear the passthrough state (state + timer),
	// even though iron-proxy for this test project isn't actually
	// running — the top-of-handler defer runs regardless of stop errors.
	require.NoError(t, c.StopVM(ctx, name, false))

	_, ok := egressPassthroughState.get(name)
	assert.False(t, ok, "/vm/stop must clear egressPassthroughState so no timer fires against a dead softnet")
}

// mustEntry returns egressPassthroughState's entry for name or fails
// the test — small helper that keeps the assertion above compact.
func mustEntry(t *testing.T, s *egressPassthroughStore, name string) egressPassthroughEntry {
	t.Helper()
	e, ok := s.get(name)
	require.True(t, ok, "expected an egressPassthroughState entry for %s", name)
	return e
}

func TestEgressStatus_ReportsPassthroughAndRestricted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logDir := t.TempDir()
	sup := supervisor.New(logDir)
	bin := filepath.Join(t.TempDir(), "tart-fake")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	tr := tart.New()
	tr.Path = bin

	srv, cleanup := newTestServerWithVM(t, sup, tr)
	defer cleanup()

	const name = "egress-status-report"
	ironProxyState.put(name, projectInfo{HTTPPort: 1, HTTPSPort: 2, DNSPort: 3})
	t.Cleanup(func() {
		ironProxyState.del(name)
		egressPassthroughState.del(name)
	})
	_, _, cleanupSock := newFakeSoftnet(t, name)
	defer cleanupSock()

	c := NewClientWithSocket(srv.socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Restricted baseline (no window active).
	before, err := c.EgressStatus(ctx, name)
	require.NoError(t, err)
	require.NotNil(t, before)
	assert.Equal(t, "restricted", before.Policy)
	assert.Nil(t, before.PassthroughExpiresAt)

	// Open a window; status reports passthrough with a future expiry.
	_, _, err = c.PassthroughEgress(ctx, name, 60)
	require.NoError(t, err)
	during, err := c.EgressStatus(ctx, name)
	require.NoError(t, err)
	require.NotNil(t, during)
	assert.Equal(t, "passthrough", during.Policy)
	require.NotNil(t, during.PassthroughExpiresAt)
	assert.WithinDuration(t, time.Now().Add(60*time.Second), *during.PassthroughExpiresAt, 5*time.Second)
}
