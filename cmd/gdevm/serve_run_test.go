package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServeEnv stands up a running httptest.Server backed by the
// real gdevm serve handler, with a test manifest + test rc files +
// a per-test logsDir. Returns (serverURL, cleanupFn, cwdDir): the
// cwdDir is a fake "repo root" that findRepo will match so /v1/run's
// manifest resolution succeeds.
func newTestServeEnv(t *testing.T) (string, func(), string) {
	t.Helper()
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))
	// Resolve symlinks (macOS t.TempDir is /var/folders/... which is
	// a symlink to /private/var/folders/...) so cwd matches guestPath
	// once findRepo resolves both sides via EvalSymlinks.
	resolved, err := filepath.EvalSymlinks(repoDir)
	require.NoError(t, err)
	repoDir = resolved

	// Manifest: one repo at repoDir with two registered commands.
	manifestPath := filepath.Join(base, "commands.json")
	manifestJSON := fmt.Sprintf(`{"repos":{"myrepo":{"guestPath":%q,"commands":["say_hi","sleep_long","exit_seven"]}}}`, repoDir)
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifestJSON), 0o644))
	t.Setenv("DEVM_COMMANDS_MANIFEST", manifestPath)

	// Stub devm.sh defining the registered functions.
	shPath := filepath.Join(base, "devm.sh")
	shBody := `
say_hi() {
  echo "hi from say_hi"
  echo "args: $*"
}
sleep_long() {
  echo "sleep_long started pid=$$"
  sleep 60
  echo "sleep_long done"
}
exit_seven() {
  echo "about to exit 7"
  exit 7
}
`
	require.NoError(t, os.WriteFile(shPath, []byte(shBody), 0o644))

	// Override seams.
	origDevmSh, origDevmMeSh, origLogsDir := guestDevmShPath, guestDevmMeShPath, logsDir
	guestDevmShPath = shPath
	guestDevmMeShPath = filepath.Join(base, "devm.me.sh") // intentionally missing — script's [ -f ] guard handles it
	logsDir = filepath.Join(base, "logs")

	reg := newRunRegistry()
	srv := httptest.NewServer(buildServeHandlerWithRoot(time.Now(), "", "/", reg))
	cleanup := func() {
		srv.Close()
		guestDevmShPath = origDevmSh
		guestDevmMeShPath = origDevmMeSh
		logsDir = origLogsDir
	}
	return srv.URL, cleanup, repoDir
}

// decodeAttachedResponse reads frames from an attached /v1/run
// response body and returns (concatenated stdout+stderr output,
// exit code, error).
func decodeAttachedResponse(t *testing.T, body *bufio.Reader) (string, int, error) {
	t.Helper()
	var out bytes.Buffer
	for {
		typ, payload, err := readFrame(body)
		if err != nil {
			return out.String(), -1, err
		}
		switch typ {
		case frameOutput:
			out.Write(payload)
		case frameExit:
			if len(payload) != 4 {
				return out.String(), -1, fmt.Errorf("malformed exit frame len=%d", len(payload))
			}
			return out.String(), int(int32(binary.BigEndian.Uint32(payload))), nil
		default:
			t.Fatalf("unknown frame type %d", typ)
		}
	}
}

func postRun(t *testing.T, serverURL string, req runRequest) *http.Response {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)
	r, err := http.NewRequest(http.MethodPost, serverURL+"/v1/run", bytes.NewReader(body))
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	return resp
}

// TestRun_Attached_StreamsOutputAndExit pins the happy path: an
// attached run streams back the function's output as frame-type 0
// chunks and ends with a frame-type 2 exit code matching the
// function's exit.
func TestRun_Attached_StreamsOutputAndExit(t *testing.T) {
	serverURL, cleanup, repoDir := newTestServeEnv(t)
	defer cleanup()

	resp := postRun(t, serverURL, runRequest{
		Cwd:  repoDir,
		Name: "say_hi",
		Args: []string{"world", "friends"},
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	out, code, err := decodeAttachedResponse(t, bufio.NewReader(resp.Body))
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "hi from say_hi")
	assert.Contains(t, out, "args: world friends")
}

// TestRun_Attached_PropagatesNonZeroExit pins exit-code preservation.
func TestRun_Attached_PropagatesNonZeroExit(t *testing.T) {
	serverURL, cleanup, repoDir := newTestServeEnv(t)
	defer cleanup()

	resp := postRun(t, serverURL, runRequest{Cwd: repoDir, Name: "exit_seven"})
	defer resp.Body.Close()
	_, code, err := decodeAttachedResponse(t, bufio.NewReader(resp.Body))
	require.NoError(t, err)
	assert.Equal(t, 7, code)
}

// TestRun_Detached_Returns201AndKeepsRunning pins that detach=true
// gets a 201 immediately and the bash child is still alive after
// the HTTP exchange completes.
func TestRun_Detached_Returns201AndKeepsRunning(t *testing.T) {
	serverURL, cleanup, repoDir := newTestServeEnv(t)
	defer cleanup()

	resp := postRun(t, serverURL, runRequest{
		Cwd: repoDir, Name: "sleep_long", Detach: true,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var r runResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&r))
	assert.Equal(t, "sleep_long", r.Name)
	assert.Greater(t, r.Pid, 0)

	// Prove the process is still alive after the POST returns.
	require.NoError(t, syscall.Kill(r.Pid, 0), "detached run must survive its HTTP request")

	// Cleanup: stop it.
	stopURL := serverURL + "/v1/stop?name=sleep_long"
	stopResp, err := http.Post(stopURL, "", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, stopResp.StatusCode)
	stopResp.Body.Close()
	// Short wait for the process to actually exit.
	time.Sleep(200 * time.Millisecond)
	assert.Error(t, syscall.Kill(r.Pid, 0), "expected process gone after /v1/stop")
}

// TestRun_ClientDisconnectStopsRun pins the attached contract: when
// the client closes the connection, the server SIGTERMs the pgroup
// and the bash child dies within gracefulStopTimeout. This is the
// Mac-side-exec-dies case translated to the server-side design.
func TestRun_ClientDisconnectStopsRun(t *testing.T) {
	serverURL, cleanup, repoDir := newTestServeEnv(t)
	defer cleanup()

	// Send the request on a context we can cancel to force the
	// disconnect. No httptest.Client timeout; we cancel manually.
	body, _ := json.Marshal(runRequest{Cwd: repoDir, Name: "sleep_long"})
	req, _ := http.NewRequest(http.MethodPost, serverURL+"/v1/run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Use a client that returns immediately as the response body
	// starts streaming (so we can read a chunk, confirm bash is
	// alive, then cancel).
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	// Read one output frame to prove bash is up; grab the pid via
	// /v1/runs so we can poll liveness after disconnect.
	br := bufio.NewReader(resp.Body)
	typ, _, err := readFrame(br)
	require.NoError(t, err)
	require.Equal(t, frameOutput, typ, "expected first frame to be output")

	// Grab the pid via /v1/runs.
	listResp, err := http.Get(serverURL + "/v1/runs")
	require.NoError(t, err)
	var rows []runSummary
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&rows))
	listResp.Body.Close()
	require.Len(t, rows, 1)
	pid := rows[0].Pid
	require.NoError(t, syscall.Kill(pid, 0), "bash child should be alive before disconnect")

	// Disconnect. Closing the body signals the server-side request
	// context cancellation.
	resp.Body.Close()

	// Wait up to gracefulStopTimeout+2s for the process to die.
	deadline := time.Now().Add(gracefulStopTimeout + 2*time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // process gone — success
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("bash child (pid %d) still alive after disconnect + grace window", pid)
}

// TestRun_ConcurrentSameNameReturns409 pins the exclusivity rule.
func TestRun_ConcurrentSameNameReturns409(t *testing.T) {
	serverURL, cleanup, repoDir := newTestServeEnv(t)
	defer cleanup()

	// Start a detached sleep_long so a slot is held.
	first := postRun(t, serverURL, runRequest{Cwd: repoDir, Name: "sleep_long", Detach: true})
	require.Equal(t, http.StatusCreated, first.StatusCode)
	first.Body.Close()
	defer func() {
		_, _ = http.Post(serverURL+"/v1/stop?name=sleep_long", "", nil)
	}()

	second := postRun(t, serverURL, runRequest{Cwd: repoDir, Name: "sleep_long", Detach: true})
	defer second.Body.Close()
	assert.Equal(t, http.StatusConflict, second.StatusCode)
}

// TestRun_UnknownNameReturns400 pins server-side manifest
// validation: the server is authoritative, so a malicious or buggy
// client can't inject arbitrary function names.
func TestRun_UnknownNameReturns400(t *testing.T) {
	serverURL, cleanup, repoDir := newTestServeEnv(t)
	defer cleanup()

	resp := postRun(t, serverURL, runRequest{Cwd: repoDir, Name: "nope_not_registered"})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, _ := readAll(resp.Body)
	assert.Contains(t, body, "not registered in repo")
}

// TestRun_CwdOutsideRepoReturns400 pins the other arm of
// server-side validation.
func TestRun_CwdOutsideRepoReturns400(t *testing.T) {
	serverURL, cleanup, _ := newTestServeEnv(t)
	defer cleanup()

	resp := postRun(t, serverURL, runRequest{Cwd: "/tmp", Name: "say_hi"})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestStop_UnknownReturns404 pins the not-found path.
func TestStop_UnknownReturns404(t *testing.T) {
	serverURL, cleanup, _ := newTestServeEnv(t)
	defer cleanup()

	resp, err := http.Post(serverURL+"/v1/stop?name=ghost", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestLogs_HistoricalServesFile pins that logs survive the run's
// own exit and can still be fetched via /v1/logs.
func TestLogs_HistoricalServesFile(t *testing.T) {
	serverURL, cleanup, repoDir := newTestServeEnv(t)
	defer cleanup()

	// Run a short attached run so we have a completed log.
	resp := postRun(t, serverURL, runRequest{Cwd: repoDir, Name: "say_hi", Args: []string{"forever"}})
	_, _, err := decodeAttachedResponse(t, bufio.NewReader(resp.Body))
	require.NoError(t, err)
	resp.Body.Close()

	// GET /v1/logs?name=say_hi — no follow.
	logResp, err := http.Get(serverURL + "/v1/logs?name=say_hi")
	require.NoError(t, err)
	defer logResp.Body.Close()
	require.Equal(t, http.StatusOK, logResp.StatusCode)
	body, _ := readAll(logResp.Body)
	assert.Contains(t, body, "hi from say_hi")
	assert.Contains(t, body, "args: forever")
}

// TestLogs_FollowStreamsLiveBytes pins that follow=true picks up
// bytes written AFTER the follower attached.
func TestLogs_FollowStreamsLiveBytes(t *testing.T) {
	serverURL, cleanup, repoDir := newTestServeEnv(t)
	defer cleanup()

	// Start a run that prints every 200ms.
	// Add a special function to the test env for this one test.
	appendTestFunction(t, "chatty", `
chatty() {
  for i in $(seq 1 10); do
    echo "tick-$i"
    sleep 0.1
  done
}
`)

	// Start it detached so it keeps writing while we read logs.
	start := postRun(t, serverURL, runRequest{Cwd: repoDir, Name: "chatty", Detach: true})
	require.Equal(t, http.StatusCreated, start.StatusCode)
	start.Body.Close()
	defer func() { _, _ = http.Post(serverURL+"/v1/stop?name=chatty", "", nil) }()

	// Open follow and read until we see tick-5 or more.
	logResp, err := http.Get(serverURL + "/v1/logs?name=chatty&follow=true")
	require.NoError(t, err)
	defer logResp.Body.Close()
	require.Equal(t, http.StatusOK, logResp.StatusCode)

	scanner := bufio.NewScanner(logResp.Body)
	seen := map[string]bool{}
	deadline := time.Now().Add(5 * time.Second)
	for scanner.Scan() && time.Now().Before(deadline) {
		line := scanner.Text()
		seen[line] = true
		if seen["tick-1"] && seen["tick-5"] {
			return // success
		}
	}
	t.Fatalf("did not see tick-1 and tick-5 in follow stream; got: %v", seen)
}

// TestRuns_ListsActiveWithPid pins the /v1/runs shape.
func TestRuns_ListsActiveWithPid(t *testing.T) {
	serverURL, cleanup, repoDir := newTestServeEnv(t)
	defer cleanup()

	start := postRun(t, serverURL, runRequest{Cwd: repoDir, Name: "sleep_long", Detach: true})
	require.Equal(t, http.StatusCreated, start.StatusCode)
	start.Body.Close()
	defer func() { _, _ = http.Post(serverURL+"/v1/stop?name=sleep_long", "", nil) }()

	resp, err := http.Get(serverURL + "/v1/runs")
	require.NoError(t, err)
	defer resp.Body.Close()
	var rows []runSummary
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "sleep_long", rows[0].Name)
	assert.True(t, rows[0].Detach)
	assert.False(t, rows[0].Exited)
	assert.Greater(t, rows[0].Pid, 0)
}

// appendTestFunction appends a bash function definition to the test
// env's devm.sh. Tests that need a custom function call this before
// their first postRun.
func appendTestFunction(t *testing.T, name, body string) {
	t.Helper()
	f, err := os.OpenFile(guestDevmShPath, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteString(body)
	require.NoError(t, err)
	// Also register in the manifest.
	manifestPath := os.Getenv("DEVM_COMMANDS_MANIFEST")
	raw, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	var m struct {
		Repos map[string]struct {
			GuestPath string   `json:"guestPath"`
			Commands  []string `json:"commands"`
		} `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(raw, &m))
	repo := m.Repos["myrepo"]
	repo.Commands = append(repo.Commands, name)
	m.Repos["myrepo"] = repo
	out, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath, out, 0o644))
}

func readAll(r interface {
	Read(p []byte) (n int, err error)
}) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			if err.Error() == "EOF" {
				return sb.String(), nil
			}
			return sb.String(), err
		}
	}
}
