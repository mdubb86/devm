// Package supervisor manages the daemon's long-lived child processes:
// per-project Tart VMs in Ship 4, and per-project iron-proxy
// instances in Ship 5. It wraps go.viam.com/utils/pexec for the
// core lifecycle and adds a setsid shim so children survive a daemon
// crash.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.viam.com/utils/pexec"
)

// ErrNotFound is returned by Stop/Status when the key isn't registered.
var ErrNotFound = errors.New("supervisor: key not found")

// Role identifies the kind of supervised child.
type Role string

const (
	RoleVM    Role = "vm"
	RoleProxy Role = "proxy" // Ship 5 iron-proxy
)

// Key is the registry key: one process per (project_id, role).
type Key struct {
	ProjectID string
	Role      Role
}

// String returns the canonical id for this key.
func (k Key) String() string {
	return fmt.Sprintf("%s/%s", k.ProjectID, k.Role)
}

// State is a snapshot for `devm status` / admin queries.
type State struct {
	Present bool // is the key registered?
	Running bool // is the process running right now?
	PID     int  // 0 if not running
}

// Supervisor manages a set of (key → managed process) entries. Two
// classes coexist:
//   - pexec-managed: spawned this daemon's lifetime via Spawn /
//     SpawnWithStdin. Get full lifecycle, auto-restart with backoff,
//     log capture.
//   - adopted: discovered post-daemon-restart via Adopt. Only the PID
//     is tracked; no auto-restart, no log capture. Stop signals via
//     SIGTERM by PID.
type Supervisor struct {
	pm      pexec.ProcessManager
	mu      sync.Mutex
	logDir  string
	adopted map[Key]int // adopted-from-prior-daemon → PID
}

// New returns a Supervisor that captures per-process logs under
// ~/Library/Logs/devm/. logDir overrides that location if non-empty.
func New(logDir string) *Supervisor {
	pm := pexec.NewProcessManager(zap.NewNop().Sugar())
	// Flip the manager into "started" mode so AddProcessFromConfig
	// actually starts the child instead of just registering it.
	_ = pm.Start(context.Background())
	return &Supervisor{
		pm:      pm,
		logDir:  defaultLogDir(logDir),
		adopted: map[Key]int{},
	}
}

// Adopt registers an externally-running process (e.g., one inherited
// from a prior daemon instance after a restart). The supervisor only
// knows its PID — no log capture, no auto-restart on crash. Stop on
// an adopted key signals SIGTERM by PID; if the process dies without
// our involvement, the next Status call surfaces it as gone.
func (s *Supervisor) Adopt(k Key, pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adopted[k] = pid
}

func defaultLogDir(override string) string {
	if override != "" {
		return override
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "devm")
}

// Spawn registers and starts a managed child. cmd is a prepared
// exec.Cmd (e.g., from tart.Run). The supervisor pre-binds
// SysProcAttr.Setsid (darwin only) so the child detaches into its own
// process group, then hands the underlying state to pexec for
// lifecycle management.
//
// Optional taps receive an io.MultiWriter fanout of the child's combined
// stdout+stderr alongside the on-disk log file. Used by the daemon to
// consume structured audit output (e.g., iron-proxy's reject records)
// without a second copy on disk. Nil taps are silently skipped.
func (s *Supervisor) Spawn(ctx context.Context, k Key, cmd *exec.Cmd, taps ...io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	applySetsid(cmd)

	if err := os.MkdirAll(s.logDir, 0700); err != nil {
		return fmt.Errorf("supervisor logDir %s: %w", s.logDir, err)
	}
	logPath := filepath.Join(s.logDir, fmt.Sprintf("%s-%s.log", k.ProjectID, k.Role))
	logWriter, err := os.OpenFile(logPath,
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("supervisor log %s: %w", logPath, err)
	}

	var out io.Writer = logWriter
	if len(taps) > 0 {
		writers := []io.Writer{logWriter}
		for _, t := range taps {
			if t != nil {
				writers = append(writers, t)
			}
		}
		if len(writers) > 1 {
			out = io.MultiWriter(writers...)
		}
	}

	backoff := newBackoff(time.Second, 30*time.Second)

	cfg := pexec.ProcessConfig{
		ID:               k.String(),
		Name:             cmd.Path,
		Args:             argsAfterPath(cmd.Args),
		CWD:              cmd.Dir,
		Environment:      envMap(cmd.Env),
		StopSignal:       syscall.SIGTERM,
		StopTimeout:      10 * time.Second,
		LogWriter:        out,
		OnUnexpectedExit: backoff.onExit,
	}

	if _, err := s.pm.AddProcessFromConfig(ctx, cfg); err != nil {
		return fmt.Errorf("supervisor.Spawn(%s): %w", k, err)
	}
	return nil
}

// Stop signals + waits for graceful shutdown. Removes the entry from
// the registry. Handles both pexec-managed and adopted entries; for
// adopted, SIGTERM is delivered by PID and ESRCH (already-dead) is
// treated as success.
func (s *Supervisor) Stop(ctx context.Context, k Key) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pid, ok := s.adopted[k]; ok {
		delete(s.adopted, k)
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}
			return fmt.Errorf("supervisor.Stop(%s): kill adopted pid %d: %w", k, pid, err)
		}
		return nil
	}
	p, ok := s.pm.RemoveProcessByID(k.String())
	if !ok {
		return fmt.Errorf("supervisor.Stop(%s): %w", k, ErrNotFound)
	}
	if err := p.Stop(); err != nil {
		return fmt.Errorf("supervisor.Stop(%s): %w", k, err)
	}
	return nil
}

// Deregister removes the entry for k from the supervisor's registry
// WITHOUT signaling the underlying process. This disables the
// OnUnexpectedExit auto-respawn for that entry, so callers that expect
// the process to exit on its own (e.g., a graceful in-guest poweroff
// triggering a `tart run` process exit) can let that happen without
// triggering a backoff-respawn storm.
//
// Callers still own the process's PID afterward — Deregister does NOT
// kill it. If the process needs to be force-terminated, the caller can
// syscall.Kill the returned PID. A PID of 0 means the process's PID
// couldn't be determined (e.g., not yet started) even though the entry
// was deregistered; that is not an error.
//
// Idempotent: unknown keys return (0, ErrNotFound) — same shape as Stop.
// Adopted entries (Supervisor.Adopt path) also cleanly deregister.
func (s *Supervisor) Deregister(k Key) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pid, ok := s.adopted[k]; ok {
		delete(s.adopted, k)
		return pid, nil
	}
	p, ok := s.pm.RemoveProcessByID(k.String())
	if !ok {
		return 0, ErrNotFound
	}
	// pexec's RemoveProcessByID only deletes the registry entry — it
	// does not signal or wait on the process, so the child (and its
	// OnUnexpectedExit hook) is already fully detached from pexec by
	// the time we get here.
	pid, err := p.UnixPid()
	if err != nil {
		return 0, nil
	}
	return pid, nil
}

// Status reports basic state for `devm status`. Handles both
// pexec-managed and adopted entries; an adopted PID that no longer
// exists is reaped from the map and reported as not present.
func (s *Supervisor) Status(k Key) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pid, ok := s.adopted[k]; ok {
		if err := syscall.Kill(pid, 0); err != nil {
			delete(s.adopted, k)
			return State{Present: false}
		}
		return State{Present: true, Running: true, PID: pid}
	}
	p, ok := s.pm.ProcessByID(k.String())
	if !ok {
		return State{Present: false}
	}
	running := p.Status() == nil
	pid := 0
	if running {
		if v, err := p.UnixPid(); err == nil {
			pid = v
		}
	}
	return State{Present: true, Running: running, PID: pid}
}

// envMap converts cmd.Env (KEY=VALUE slice) to the map[string]string
// that pexec.ProcessConfig.Environment expects. When cmd.Env is empty,
// the daemon's environment is forwarded — pexec builds the child's
// env solely from this map (no implicit parent inheritance).
func envMap(env []string) map[string]string {
	if len(env) == 0 {
		env = os.Environ()
	}
	m := make(map[string]string, len(env))
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				m[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	return m
}

// argsAfterPath strips the leading binary path from cmd.Args.
// exec.Cmd.Args[0] is the binary; pexec.ProcessConfig.Args wants
// just the remaining arguments.
func argsAfterPath(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	return append([]string(nil), args[1:]...)
}

// backoffState implements exponential restart backoff: base → 2x →
// 4x ... capped. Resets to base if the process stayed up >30s before
// crashing.
type backoffState struct {
	mu        sync.Mutex
	base      time.Duration
	cap       time.Duration
	delay     time.Duration
	lastStart time.Time
}

func newBackoff(base, capDelay time.Duration) *backoffState {
	return &backoffState{base: base, cap: capDelay}
}

// onExit is the pexec UnexpectedExitHandler callback. exitCode is the
// process's exit code. Returns true to request a restart.
func (b *backoffState) onExit(_ context.Context, exitCode int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	stableFor := now.Sub(b.lastStart)
	switch {
	case stableFor > 30*time.Second:
		b.delay = b.base
	case b.delay == 0:
		b.delay = b.base
	default:
		b.delay *= 2
		if b.delay > b.cap {
			b.delay = b.cap
		}
	}
	_ = exitCode
	time.Sleep(b.delay)
	b.lastStart = time.Now()
	return true
}
