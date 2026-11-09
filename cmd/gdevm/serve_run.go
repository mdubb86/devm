package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// logsDir holds per-run merged stdout+stderr. Package var so tests
// can override to a t.TempDir() without colliding with real guest
// state. Production value is set by the service unit's LogsDirectory=
// directive and defaults to /var/log/gdevm.
var logsDir = func() string {
	if d := os.Getenv("DEVM_GDEVM_LOGS_DIR"); d != "" {
		return d
	}
	return "/var/log/gdevm"
}()

// Frame types for the /v1/run streaming response body. One byte of
// type + 4 bytes big-endian length + payload. stdout and stderr are
// merged under frameOutput for v1; a reader that needs separation
// can split via `2>/dev/null` inside the function.
const (
	frameOutput byte = 0
	frameExit   byte = 2
)

// gracefulStopTimeout is the SIGTERM→SIGKILL window applied when
// stopping a run, either from /v1/stop or from an attached client
// disconnect. Long enough for a well-behaved service to run its own
// cleanup; short enough that a wedged process doesn't block the
// operator's follow-up `devm stop`.
const gracefulStopTimeout = 5 * time.Second

// run is one active or recently-exited bash invocation of a named
// function from the project's commands manifest.
type run struct {
	name      string
	cmd       *exec.Cmd
	pgid      int
	startedAt time.Time
	detach    bool
	logPath   string

	// attachedCh carries output copies to the attached client, if
	// one is currently attached. Buffered so a slow network doesn't
	// block the fan-out goroutine; the forwarder drops bytes when
	// the buffer is full rather than back-pressure bash.
	//
	// Written under mu by the attached-run handler at attach and
	// detach; read without the lock inside the forwarder goroutine
	// (which snapshots via load into a local) and inside the
	// handler itself. The forwarder sends via a non-blocking select,
	// so a concurrent close() by the handler is safe as long as the
	// handler's detach runs after the forwarder has visibility of
	// the nil swap.
	attachedMu sync.Mutex
	attachedCh chan []byte

	// Signaled once bash has exited and its exit code is settled.
	// Readers of exitCode must wait on this channel first.
	exitDone chan struct{}
	exitCode int

	// reaperDone is closed once the reaper goroutine (first-party
	// runs) has fully returned, including state-file cleanup. Nil
	// for orphan runs that had no reaper. Tests wait on it before
	// restoring globals the reaper reads.
	reaperDone chan struct{}
	// pollerDone is closed once pollOrphanExit returns, including
	// state-file cleanup. Nil for first-party runs. Tests wait on it
	// before restoring globals the poller reads.
	pollerDone chan struct{}
}

// runRegistry tracks the in-flight runs by name. Mutex-guarded map,
// one run per name (concurrent same-name /v1/run returns 409).
type runRegistry struct {
	mu   sync.Mutex
	runs map[string]*run
}

func newRunRegistry() *runRegistry {
	return &runRegistry{runs: make(map[string]*run)}
}

// startExclusive registers r under name only if no live run with
// that name exists. Returns an existing run if one is already live,
// so the caller can distinguish "collision" from a clean start.
func (reg *runRegistry) startExclusive(name string, r *run) (existing *run, ok bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if prior, present := reg.runs[name]; present {
		// A prior run that has already exited is not a collision —
		// clean it up and let the new run take the slot.
		select {
		case <-prior.exitDone:
			delete(reg.runs, name)
		default:
			return prior, false
		}
	}
	reg.runs[name] = r
	return nil, true
}

// unregister removes name from the registry. Idempotent.
func (reg *runRegistry) unregister(name string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	delete(reg.runs, name)
}

// lookup returns the registered run for name, or nil if absent.
func (reg *runRegistry) lookup(name string) *run {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return reg.runs[name]
}

// list returns a snapshot of registered runs for /v1/runs.
func (reg *runRegistry) list() []*run {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	out := make([]*run, 0, len(reg.runs))
	for _, r := range reg.runs {
		out = append(out, r)
	}
	return out
}

// runRequest is the /v1/run POST body shape. Cwd is sent because
// gdevm serve runs under systemd (its own cwd is /) while the
// client runs under the user's shell — cwd tells the server which
// repo the client is "in" for manifest resolution.
type runRequest struct {
	Cwd    string            `json:"cwd"`
	Name   string            `json:"name"`
	Args   []string          `json:"args,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Detach bool              `json:"detach,omitempty"`
}

// runResponse is the body of a 201 Created after a detached start.
// For attached runs the body is the framed stream; this type only
// serializes to the client when detach=true.
type runResponse struct {
	Name      string    `json:"name"`
	Pid       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// handleRun implements POST /v1/run. See the design notes at the top
// of the file for stream framing and lifecycle.
func handleRun(reg *runRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req runRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "parse body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if req.Cwd == "" {
			http.Error(w, "cwd is required", http.StatusBadRequest)
			return
		}

		// Server-side validation against the commands manifest.
		// Matches the pre-fix gdevm run shape: same manifest, same
		// findRepo, same registered check — authoritative here so a
		// malicious client can't inject arbitrary function names.
		m, err := loadManifest()
		if err != nil {
			http.Error(w, "load manifest: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cwd := req.Cwd
		if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = resolved
		}
		repoName, guestPath, ok := findRepo(m, cwd)
		if !ok {
			http.Error(w, "cwd is not inside a registered repo", http.StatusBadRequest)
			return
		}
		if !registered(m.Repos[repoName].Commands, req.Name) {
			http.Error(w, fmt.Sprintf("command %s not registered in repo %s", req.Name, repoName), http.StatusBadRequest)
			return
		}

		// Prepare run state before spawning so a collision rejects
		// without leaving a process behind.
		r1 := &run{
			name:       req.Name,
			startedAt:  time.Now().UTC(),
			detach:     req.Detach,
			logPath:    filepath.Join(logsDir, req.Name+".log"),
			exitDone:   make(chan struct{}),
			reaperDone: make(chan struct{}),
		}
		if existing, ok := reg.startExclusive(req.Name, r1); !ok {
			http.Error(w, fmt.Sprintf("a run named %q is already active (pid %d)", existing.name, leaderPid(existing)), http.StatusConflict)
			return
		}

		// Everything from here onward either succeeds AND leaves r1
		// in the registry, or unregisters r1 before returning an
		// error to the client.
		if err := os.MkdirAll(logsDir, 0o755); err != nil {
			reg.unregister(req.Name)
			http.Error(w, "mkdir logs dir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		logFile, err := os.OpenFile(r1.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			reg.unregister(req.Name)
			http.Error(w, "open log file: "+err.Error(), http.StatusInternalServerError)
			return
		}

		scriptBody := buildRunScript(guestPath, req.Name)
		bashArgv := append([]string{"bash", "-c", scriptBody, "gdevm-run"}, req.Args...)
		cmd := exec.Command("/bin/bash", bashArgv[1:]...)
		cmd.Path = "/bin/bash"
		cmd.Args = bashArgv
		cmd.Env = envMapToSlice(req.Env)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		pipeR, pipeW, err := os.Pipe()
		if err != nil {
			logFile.Close()
			reg.unregister(req.Name)
			http.Error(w, "pipe: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cmd.Stdout = pipeW
		cmd.Stderr = pipeW
		if err := cmd.Start(); err != nil {
			pipeR.Close()
			pipeW.Close()
			logFile.Close()
			reg.unregister(req.Name)
			http.Error(w, "start bash: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Close the parent's write end so EOF reaches the forwarder
		// when bash exits.
		pipeW.Close()
		r1.cmd = cmd
		r1.pgid = cmd.Process.Pid // first process in the pgroup is its own leader

		// Persist rediscovery state right after Start+pgid so a
		// crash between here and the handler returning still leaves
		// a usable state file. Non-fatal: if the write fails the
		// run proceeds, we just lose the ability to rediscover it
		// after a restart.
		if err := writeRunState(r1); err != nil {
			log.Printf("writeRunState %q: %v (rediscovery after restart will miss this run)", r1.name, err)
		}

		// Output forwarder: fan-out from pipeR to log file (always)
		// and to r1.attachedCh (if set, non-blocking drop on full).
		go func() {
			defer pipeR.Close()
			defer logFile.Close()
			buf := make([]byte, 4096)
			for {
				n, err := pipeR.Read(buf)
				if n > 0 {
					_, _ = logFile.Write(buf[:n])
					r1.attachedMu.Lock()
					ch := r1.attachedCh
					r1.attachedMu.Unlock()
					if ch != nil {
						chunk := make([]byte, n)
						copy(chunk, buf[:n])
						select {
						case ch <- chunk:
						default:
							// Attached buffer full; drop rather than block.
							// Log file still records.
						}
					}
				}
				if err != nil {
					return
				}
			}
		}()

		// Reaper: waits for bash, records exit code, signals
		// exitDone. Starts immediately so even detached runs settle
		// their exit code when the process ends.
		go func() {
			defer close(r1.reaperDone)
			err := cmd.Wait()
			code := 0
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					code = exitErr.ExitCode()
				} else {
					code = -1
				}
			}
			r1.exitCode = code
			// Clean up the rediscovery state file BEFORE signaling
			// exitDone — otherwise a waiter unblocking on exitDone
			// might read logsDir after a test has restored it to a
			// different value, and removeRunState would target the
			// wrong directory. Prod effect is identical (we are not
			// racing anything there); test observability is what
			// requires the ordering.
			removeRunState(r1.name)
			close(r1.exitDone)
			// Leave r1 in the registry briefly so `gdevm runs`
			// right after exit can still report the final state;
			// startExclusive will reap on the next run with the
			// same name.
		}()

		// Detached: respond 201 and return. Forwarder + reaper keep
		// working in the background.
		if req.Detach {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(runResponse{
				Name:      r1.name,
				Pid:       r1.cmd.Process.Pid,
				StartedAt: r1.startedAt,
			})
			return
		}

		// Attached: stream frames to the client. Register the
		// attached channel so the forwarder fans output into our
		// stream.
		attachedCh := make(chan []byte, 64)
		r1.attachedMu.Lock()
		r1.attachedCh = attachedCh
		r1.attachedMu.Unlock()
		defer func() {
			r1.attachedMu.Lock()
			r1.attachedCh = nil
			r1.attachedMu.Unlock()
		}()

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		streamAttached(w, flusher, r1, attachedCh, r.Context())
	}
}

// streamAttached forwards bytes from attachedCh to w as framed
// chunks and handles the three terminal conditions: client
// disconnected (SIGTERM the run), bash exited (send exit frame and
// return), or stream error (return).
func streamAttached(w io.Writer, flusher http.Flusher, r1 *run, attachedCh <-chan []byte, ctx interface {
	Done() <-chan struct{}
}) {
	for {
		select {
		case chunk, ok := <-attachedCh:
			if !ok {
				return
			}
			if err := writeFrame(w, frameOutput, chunk); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case <-r1.exitDone:
			// Drain any remaining buffered bytes so we don't drop
			// the last chunk bash wrote before exiting. Bounded
			// by channel buffer capacity.
			for {
				select {
				case chunk := <-attachedCh:
					_ = writeFrame(w, frameOutput, chunk)
				default:
					code := make([]byte, 4)
					binary.BigEndian.PutUint32(code, uint32(r1.exitCode)) //nolint:gosec // narrow range
					_ = writeFrame(w, frameExit, code)
					if flusher != nil {
						flusher.Flush()
					}
					return
				}
			}
		case <-ctx.Done():
			// Client disconnected while the run was still alive.
			// Attached runs stop on disconnect; detached runs do
			// not — and we're only here in attached mode.
			stopRun(r1)
			return
		}
	}
}

// writeFrame emits one length-prefixed frame to w.
func writeFrame(w io.Writer, typ byte, payload []byte) error {
	header := make([]byte, 5)
	header[0] = typ
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload))) //nolint:gosec // payload size bounded
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// stopRun sends SIGTERM to the run's process group, waits up to
// gracefulStopTimeout, then SIGKILL. Idempotent against an
// already-exited run (the kills will return ESRCH, which we ignore).
func stopRun(r1 *run) {
	_ = syscall.Kill(-r1.pgid, syscall.SIGTERM)
	select {
	case <-r1.exitDone:
		return
	case <-time.After(gracefulStopTimeout):
	}
	_ = syscall.Kill(-r1.pgid, syscall.SIGKILL)
	<-r1.exitDone
}

// envMapToSlice converts a {KEY: val} map into the []string form
// exec.Cmd.Env expects. Deterministic order for readable tests.
func envMapToSlice(m map[string]string) []string {
	if m == nil {
		return os.Environ()
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// loadManifest reads the commands manifest from
// DEVM_COMMANDS_MANIFEST or the compiled-in default. Separated so
// the server and the client can share the exact lookup.
func loadManifest() (manifest, error) {
	path := os.Getenv("DEVM_COMMANDS_MANIFEST")
	if path == "" {
		path = defaultManifest
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return manifest{}, err
	}
	return m, nil
}

// readFrame reads one frame from r. Returns the type byte, the
// payload, or an error. Used by the client and by tests.
func readFrame(r *bufio.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(header[1:])
	payload := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	return header[0], payload, nil
}
