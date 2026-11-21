package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOrphanTestEnv sets up globals so orphan rediscovery can be
// exercised safely under `go test -race`:
//
//   - logsDir → a per-test temp dir, so writes don't touch
//     production /var/log/gdevm;
//   - orphanPollInterval → 20ms, so the poller's death detection
//     lands inside a test deadline.
//
// Returns a fresh registry. The helper registers t.Cleanup that waits
// for every pollerDone / reaperDone in the registry to close BEFORE
// restoring the globals — those goroutines read logsDir and
// orphanPollInterval via removeRunState and the ticker respectively,
// so restoring first would race. t.Cleanup runs after the test
// body's defers, so tests use `defer kill()` on their spawned
// pgroups; by the time the drain runs, the pgroups are gone, the
// pollers have seen ESRCH, and the drain is a no-op.
func newOrphanTestEnv(t *testing.T) *runRegistry {
	t.Helper()
	base := t.TempDir()
	origLogsDir := logsDir
	logsDir = filepath.Join(base, "logs")
	origPoll := orphanPollInterval
	orphanPollInterval = 20 * time.Millisecond
	reg := newRunRegistry()
	t.Cleanup(func() {
		for _, r := range reg.list() {
			if r.pollerDone != nil {
				<-r.pollerDone
			}
			if r.reaperDone != nil {
				<-r.reaperDone
			}
		}
		logsDir = origLogsDir
		orphanPollInterval = origPoll
	})
	return reg
}

// spawnPgroupChild starts a long-lived child in its own process group
// via setpgid + /bin/sleep, mimicking what the real serve_run path
// produces. Returns pgid (= child pid, since it is its own pgroup
// leader) and a cleanup that SIGKILLs the pgroup on test end.
//
// The reaper goroutine mirrors what PID 1 (init/systemd) does for a
// real orphaned bash in prod: pick up SIGCHLD and wait() immediately.
// Without it the test's child becomes a zombie after SIGTERM, and
// Kill(-pgid, 0) keeps returning nil while the zombie sits in the
// proc table — pollOrphanExit never sees ESRCH and stopRun blocks
// forever on exitDone.
func spawnPgroupChild(t *testing.T, seconds int) (pgid int, cleanup func()) {
	t.Helper()
	cmd := exec.Command("/bin/sleep", fmt.Sprintf("%d", seconds))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	pgid = cmd.Process.Pid
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()
	cleanup = func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-waited
	}
	return pgid, cleanup
}

// writeTestState writes a state file for `name` at the current
// logsDir (which the caller must have already pointed at a temp dir).
// pgid=0 means "write the file but reference a non-existent pgroup"
// (the caller passes a sentinel value that is known to be dead).
func writeTestState(t *testing.T, name string, pgid int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(logsDir, 0o755))
	body, err := json.Marshal(runState{
		Version:   runStateVersion,
		Name:      name,
		Pgid:      pgid,
		StartedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(stateFileForName(name), body, 0o644))
}

// TestRediscover_LivePgidRegisters: a state file pointing at a live
// pgroup produces an orphan row in the registry, with cmd=nil and the
// pgid preserved.
func TestRediscover_LivePgidRegisters(t *testing.T) {
	reg := newOrphanTestEnv(t)

	pgid, kill := spawnPgroupChild(t, 30)
	defer kill()

	writeTestState(t, "livesleeper", pgid)

	rediscoverRuns(reg)

	r := reg.lookup("livesleeper")
	require.NotNil(t, r, "live pgid should have produced a registry row")
	assert.True(t, isOrphan(r), "rediscovered row must be an orphan (cmd=nil)")
	assert.Equal(t, pgid, r.pgid)
	assert.True(t, r.detach, "orphans are detached by construction")
	select {
	case <-r.exitDone:
		t.Fatal("exitDone should still be open — the pgroup is alive")
	default:
	}
}

// TestRediscover_DeadPgidCleansState: a state file whose pgid is
// already gone leaves nothing in the registry and removes the file.
func TestRediscover_DeadPgidCleansState(t *testing.T) {
	reg := newOrphanTestEnv(t)

	// Spawn and reap a child so we have a pgid that is guaranteed to
	// have existed and now be gone (safer than picking a random int
	// that might collide with an unrelated live process). true(1) is
	// at /bin/true on linux and /usr/bin/true on macOS — resolve via
	// PATH to avoid hardcoding either.
	truePath, err := exec.LookPath("true")
	require.NoError(t, err)
	cmd := exec.Command(truePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	deadPgid := cmd.Process.Pid
	require.NoError(t, cmd.Wait())

	writeTestState(t, "ghost", deadPgid)

	rediscoverRuns(reg)

	assert.Nil(t, reg.lookup("ghost"))
	_, statErr := os.Stat(stateFileForName("ghost"))
	assert.True(t, os.IsNotExist(statErr), "dead state file should be removed")
}

// TestRediscover_InvalidJSONRemovesFile: a corrupted state file is
// removed (otherwise it would fail every rediscovery pass forever).
func TestRediscover_InvalidJSONRemovesFile(t *testing.T) {
	reg := newOrphanTestEnv(t)

	require.NoError(t, os.MkdirAll(logsDir, 0o755))
	require.NoError(t, os.WriteFile(stateFileForName("broken"), []byte("not json {{"), 0o644))

	rediscoverRuns(reg)

	assert.Nil(t, reg.lookup("broken"))
	_, err := os.Stat(stateFileForName("broken"))
	assert.True(t, os.IsNotExist(err), "broken state file should be removed")
}

// TestRediscover_VersionMismatchRemovesFile: a schema bump is signaled
// by the file being removed — operators lose rediscovery for runs
// started before the bump, which is explicit and self-healing on the
// next run.
func TestRediscover_VersionMismatchRemovesFile(t *testing.T) {
	reg := newOrphanTestEnv(t)

	require.NoError(t, os.MkdirAll(logsDir, 0o755))
	body, _ := json.Marshal(map[string]any{
		"version":    9999,
		"name":       "future",
		"pgid":       os.Getpid(),
		"started_at": time.Now().UTC(),
	})
	require.NoError(t, os.WriteFile(stateFileForName("future"), body, 0o644))

	rediscoverRuns(reg)

	assert.Nil(t, reg.lookup("future"))
	_, err := os.Stat(stateFileForName("future"))
	assert.True(t, os.IsNotExist(err))
}

// TestRediscover_StopKillsOrphan: a rediscovered orphan can be
// stopped via /v1/stop; stopRun terminates the pgroup and the poller
// signals exitDone within orphanPollInterval.
func TestRediscover_StopKillsOrphan(t *testing.T) {
	reg := newOrphanTestEnv(t)

	pgid, kill := spawnPgroupChild(t, 30)
	defer kill() // belt-and-suspenders if the test fails before stop

	writeTestState(t, "stoppable", pgid)

	rediscoverRuns(reg)
	require.NotNil(t, reg.lookup("stoppable"))

	// Mount the stop handler and fire.
	srv := httptest.NewServer(buildServeHandlerWithRoot(time.Now(), "", "/", reg))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/stop?name=stoppable", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Process gone — Kill(pgid, 0) returns ESRCH.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pgid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pgid %d still alive after /v1/stop + grace", pgid)
}

// TestRediscover_PollerMarksExitedOnExternalDeath: killing the
// pgroup out-of-band (not via /v1/stop) is detected by pollOrphanExit
// within one orphanPollInterval tick and exitDone is signaled.
func TestRediscover_PollerMarksExitedOnExternalDeath(t *testing.T) {
	reg := newOrphanTestEnv(t)

	pgid, kill := spawnPgroupChild(t, 30)
	writeTestState(t, "external", pgid)

	rediscoverRuns(reg)
	r := reg.lookup("external")
	require.NotNil(t, r)

	// Kill the pgroup externally — the poller's next tick should see
	// ESRCH and close exitDone.
	kill()

	select {
	case <-r.exitDone:
		assert.Equal(t, -1, r.exitCode, "orphan exit code is unknown and recorded as -1")
	case <-time.After(2 * time.Second):
		t.Fatal("exitDone not signaled within 2s of external kill")
	}
	// Poller also cleans up the state file on exit.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stateFileForName("external")); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("state file still present after orphan exit")
}

// TestRunStart_WritesStateFile: a /v1/run POST creates a state file
// with the live pgid, proving the rediscovery path has what it needs.
func TestRunStart_WritesStateFile(t *testing.T) {
	serverURL, cleanup, repoDir := newTestServeEnv(t)
	defer cleanup()

	resp := postRun(t, serverURL, runRequest{Cwd: repoDir, Name: "sleep_long", Detach: true})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var r runResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
	defer func() { _, _ = http.Post(serverURL+"/v1/stop?name=sleep_long", "", nil) }()

	body, err := os.ReadFile(stateFileForName("sleep_long"))
	require.NoError(t, err, "state file must exist after successful /v1/run")
	var s runState
	require.NoError(t, json.Unmarshal(body, &s))
	assert.Equal(t, runStateVersion, s.Version)
	assert.Equal(t, "sleep_long", s.Name)
	assert.Greater(t, s.Pgid, 0, "pgid must be recorded")
}

// TestRunExit_RemovesStateFile: after a short-lived run completes,
// the reaper goroutine removes the state file so it doesn't haunt
// the next rediscovery pass.
func TestRunExit_RemovesStateFile(t *testing.T) {
	serverURL, cleanup, repoDir := newTestServeEnv(t)
	defer cleanup()

	resp := postRun(t, serverURL, runRequest{Cwd: repoDir, Name: "say_hi", Args: []string{"world"}})
	_, _, err := decodeAttachedResponse(t, bufio.NewReader(resp.Body))
	require.NoError(t, err)
	resp.Body.Close()

	// Reaper runs concurrently; give it a brief window to finish.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stateFileForName("say_hi")); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("state file not removed after run exit")
}

// TestListRuns_MarksOrphanAndExitUnknown: /v1/runs shows a
// rediscovered orphan with orphan=true, and after its pgroup dies
// externally the row reports exit_code=-1.
func TestListRuns_MarksOrphanAndExitUnknown(t *testing.T) {
	reg := newOrphanTestEnv(t)

	pgid, kill := spawnPgroupChild(t, 30)
	writeTestState(t, "visible", pgid)

	rediscoverRuns(reg)
	srv := httptest.NewServer(buildServeHandlerWithRoot(time.Now(), "", "/", reg))
	defer srv.Close()

	// Live state.
	resp, err := http.Get(srv.URL + "/v1/runs")
	require.NoError(t, err)
	var rows []runSummary
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
	resp.Body.Close()
	require.Len(t, rows, 1)
	assert.Equal(t, "visible", rows[0].Name)
	assert.True(t, rows[0].Orphan, "rediscovered row must be marked orphan")
	assert.True(t, rows[0].Detach)
	assert.False(t, rows[0].Exited)
	assert.Equal(t, pgid, rows[0].Pid, "pid column should be the pgroup leader")

	// Kill externally and re-list once the poller has caught up.
	kill()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp2, err := http.Get(srv.URL + "/v1/runs")
		require.NoError(t, err)
		var rows2 []runSummary
		require.NoError(t, json.NewDecoder(resp2.Body).Decode(&rows2))
		resp2.Body.Close()
		if len(rows2) == 1 && rows2[0].Exited {
			assert.Equal(t, -1, rows2[0].ExitCode, "orphan exit is unknown")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("orphan row never reflected exit")
}

// TestRediscover_IgnoresNonStateFiles: unrelated files in logsDir
// (log files, in-flight .tmp) are not treated as state.
func TestRediscover_IgnoresNonStateFiles(t *testing.T) {
	reg := newOrphanTestEnv(t)

	require.NoError(t, os.MkdirAll(logsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(logsDir, "foo.log"), []byte("output"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(logsDir, "foo.state.tmp"), []byte("{}"), 0o644))

	rediscoverRuns(reg)
	assert.Empty(t, reg.list())

	// Sibling files preserved untouched.
	_, err := os.Stat(filepath.Join(logsDir, "foo.log"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(logsDir, "foo.state.tmp"))
	assert.NoError(t, err)
}

// TestRediscover_Idempotent: running rediscovery twice in a row
// leaves one row, not two — the second pass sees the registry already
// has the name and skips cleanly.
func TestRediscover_Idempotent(t *testing.T) {
	reg := newOrphanTestEnv(t)

	pgid, kill := spawnPgroupChild(t, 30)
	defer kill()
	writeTestState(t, "stable", pgid)

	rediscoverRuns(reg)
	rediscoverRuns(reg)
	assert.Len(t, reg.list(), 1)
}

// Belt-and-suspenders: strings must not accidentally match
// unexpected file extensions. Keeps a surprise refactor honest.
func TestStateFilePath_HasDotStateSuffix(t *testing.T) {
	assert.True(t, strings.HasSuffix(stateFileForName("foo"), "/foo.state"))
}
