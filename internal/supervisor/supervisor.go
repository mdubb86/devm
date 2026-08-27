// Package supervisor manages the daemon's long-lived child processes:
// per-project Tart VMs in Ship 4, and per-project iron-proxy
// instances in Ship 5. It wraps go.viam.com/utils/pexec for the
// core lifecycle.
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
	"sync/atomic"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.viam.com/utils/pexec"
)

// adoptedStopGrace is how long Stop's adopted-path waits for SIGTERM
// to actually kill the process before escalating to SIGKILL. Matches
// pexec's StopTimeout (see ProcessConfig.StopTimeout below) so shutdown
// budget is uniform across managed and adopted entries — callers like
// apply-iron-proxy that immediately re-bind the process's ports need
// this to be a real wait, not a fire-and-forget SIGTERM.
const adoptedStopGrace = 10 * time.Second

// sigkillGrace is the additional wait after SIGKILL escalation. SIGKILL
// is instant at the kernel level, but user-space visible port release
// on macOS can take a few hundred ms in pathological cases.
const sigkillGrace = 2 * time.Second

// ErrNotFound is returned by Stop/Status when the key isn't registered.
var ErrNotFound = errors.New("supervisor: key not found")

// Role identifies the kind of supervised child.
type Role string

const (
	RoleVM      Role = "vm"
	RoleProxy   Role = "proxy" // Ship 5 iron-proxy
	RoleMutagen Role = "mutagen"
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
//     is tracked; no auto-restart, no log capture.
//
// For iron-proxy, pexec-managed entries wrap the child in a setsid
// shim (see internal/setsidshim). The shim is what pexec sees; the
// actual iron-proxy is the shim's grandchild in a different session.
// Stop signals iron-proxy's PID directly (not the shim) — the shim
// deliberately ignores SIGTERM so launchd's bootout of the daemon
// can't reach through it to kill iron-proxy. Callers who spawn via
// a shim must therefore call SetChildPID after Spawn to teach the
// supervisor the grandchild PID; Adopt already records the correct
// PID (post-restart, the shim's grandchild is what DiscoverIronProxies
// finds by matching the iron-proxy binary in argv[0]).
type Supervisor struct {
	pm             pexec.ProcessManager
	mu             sync.Mutex
	logDir         string
	adopted        map[Key]int // adopted-from-prior-daemon → PID
	childPIDs      map[Key]int // pexec-managed spawn's grandchild (real iron-proxy behind the shim) → PID
	disableRestart sync.Map    // key.String() → *atomic.Bool; gates OnUnexpectedExit's respawn
}

// New returns a Supervisor that captures per-process logs under
// logDir. Callers pass identity.Config.LogDir() in production; tests
// pass t.TempDir().
func New(logDir string) *Supervisor {
	pm := pexec.NewProcessManager(zap.NewNop().Sugar())
	// Flip the manager into "started" mode so AddProcessFromConfig
	// actually starts the child instead of just registering it.
	_ = pm.Start(context.Background())
	return &Supervisor{
		pm:        pm,
		logDir:    logDir,
		adopted:   map[Key]int{},
		childPIDs: map[Key]int{},
	}
}

// SetChildPID records the grandchild PID for a pexec-managed entry
// whose direct pexec-visible process is a wrapping shim (iron-proxy
// via internal/setsidshim). Called by the spawner AFTER Spawn returns
// and the grandchild has been discovered (typically via
// serviceapi.DiscoverIronProxies). Overwrites any prior value for the
// key. Callers who do NOT use a shim (VMs via tart.Run) need not call
// this — Stop falls back to pexec's own Stop, which signals the direct
// child.
func (s *Supervisor) SetChildPID(k Key, pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.childPIDs[k] = pid
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

// Spawn registers and starts a managed child. cmd is a prepared
// exec.Cmd (e.g., from tart.Run), then hands the underlying state to
// pexec for lifecycle management.
//
// Detaching a child from the daemon's session (so it survives the
// daemon's own death) is not done here — it's handled at a higher
// level via a setsid shim for iron-proxy specifically; see
// internal/setsidshim + SpawnIronProxy.
//
// Optional taps receive an io.MultiWriter fanout of the child's combined
// stdout+stderr alongside the on-disk log file. Used by the daemon to
// consume structured audit output (e.g., iron-proxy's reject records)
// without a second copy on disk. Nil taps are silently skipped.
func (s *Supervisor) Spawn(ctx context.Context, k Key, cmd *exec.Cmd, taps ...io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	// disable gates the backoff-respawn: DisableRestart(k) flips it before
	// an expected exit (e.g. a guest's in-guest `systemctl poweroff` making
	// `tart run` exit on its own), so the OnUnexpectedExit callback below
	// short-circuits to "don't restart" instead of delegating to backoff.
	// A fresh *atomic.Bool per Spawn — stored before AddProcessFromConfig
	// starts the child, so there is no window where a fast-exiting child
	// could hit onUnexpectedExit before the gate exists.
	disable := &atomic.Bool{}
	s.disableRestart.Store(k.String(), disable)

	cfg := pexec.ProcessConfig{
		ID:          k.String(),
		Name:        cmd.Path,
		Args:        argsAfterPath(cmd.Args),
		CWD:         cmd.Dir,
		Environment: envMap(cmd.Env),
		StopSignal:  syscall.SIGTERM,
		StopTimeout: 10 * time.Second,
		LogWriter:   out,
		OnUnexpectedExit: func(ctx context.Context, exitCode int) bool {
			if disable.Load() {
				return false // expected exit — do not respawn
			}
			return backoff.onExit(ctx, exitCode)
		},
	}

	if _, err := s.pm.AddProcessFromConfig(ctx, cfg); err != nil {
		s.disableRestart.Delete(k.String())
		return fmt.Errorf("supervisor.Spawn(%s): %w", k, err)
	}
	return nil
}

// Stop signals + waits for graceful shutdown. Removes the entry from
// the registry.
//
// Two disposition classes:
//
//   - Iron-proxy (pexec-managed via a setsid shim, OR adopted from a
//     prior daemon): a tracked child PID exists (childPIDs[k] for
//     pexec-managed, adopted[k] for adopted). Signal that PID
//     directly with SIGTERM — do NOT signal the shim. The shim
//     ignores SIGTERM by design so launchd's bootout can't reach
//     iron-proxy through it; the flip side is that we must reach
//     iron-proxy ourselves here. Wait for actual death (ports
//     released before apply-iron-proxy rebinds), escalate to SIGKILL
//     on grace exhaustion. For pexec-managed, the shim's Wait()
//     returns naturally once iron-proxy dies; disable restart first
//     so pexec's OnUnexpectedExit doesn't respawn it.
//
//   - VMs / anything without a tracked child PID: fall back to
//     pexec's own Stop, which signals the direct child.
//
// ESRCH (already dead) is treated as success in both paths.
func (s *Supervisor) Stop(ctx context.Context, k Key) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.disableRestart.Delete(k.String())

	// Prefer adopted → childPIDs, so a key that was adopted and later
	// re-spawned (unlikely today, but defensible) uses the freshest PID.
	pid, wasAdopted := s.adopted[k]
	if !wasAdopted {
		pid = s.childPIDs[k]
	}

	if pid != 0 {
		// Suppress pexec's respawn for pexec-managed entries: the shim
		// will exit naturally once iron-proxy exits, and pexec's
		// OnUnexpectedExit must not treat that as a crash to restart.
		// No-op for adopted entries (no disable gate registered).
		if v, ok := s.disableRestart.Load(k.String()); ok {
			v.(*atomic.Bool).Store(true)
		}

		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			if !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("supervisor.Stop(%s): kill pid %d: %w", k, pid, err)
			}
		} else if !waitProcessGone(pid, adoptedStopGrace) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			_ = waitProcessGone(pid, sigkillGrace)
		}

		if wasAdopted {
			delete(s.adopted, k)
			return nil
		}
		delete(s.childPIDs, k)
		// Reap the pexec entry (shim). RemoveProcessByID takes it out
		// of pexec's registry; the shim has (or will imminently) exit
		// naturally as its Wait() on iron-proxy returns. p.Stop is a
		// no-op if the shim's already gone; if not, it sends SIGTERM
		// (which the shim ignores) then SIGKILL after StopTimeout.
		if p, ok := s.pm.RemoveProcessByID(k.String()); ok {
			_ = p.Stop()
		}
		return nil
	}

	// No tracked child PID — pexec's direct child IS the process to
	// stop (VMs via tart.Run).
	p, ok := s.pm.RemoveProcessByID(k.String())
	if !ok {
		return fmt.Errorf("supervisor.Stop(%s): %w", k, ErrNotFound)
	}
	if err := p.Stop(); err != nil {
		return fmt.Errorf("supervisor.Stop(%s): %w", k, err)
	}
	return nil
}

// DisableRestart marks k's supervised process as expected to exit: the
// next (or currently pending) OnUnexpectedExit callback for this key
// returns false instead of delegating to the backoff, so pexec does not
// respawn it. Callers invoke this immediately before triggering an
// expected-but-external exit — e.g. an in-guest `systemctl poweroff`
// that makes `tart run` exit on its own — so that natural exit isn't
// misread as a crash and respawned.
//
// Idempotent and a no-op for unknown or already-stopped keys (nothing
// to gate). The flag is cleared on the next Stop or Spawn for the same
// key.
func (s *Supervisor) DisableRestart(k Key) {
	if v, ok := s.disableRestart.Load(k.String()); ok {
		v.(*atomic.Bool).Store(true)
	}
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

// waitProcessGone polls kill(pid, 0) until it returns ESRCH (no such
// process) or the timeout expires. Returns true iff the process was
// observed gone. 50ms poll interval — small enough that a fast-exiting
// process doesn't idle the caller, coarse enough not to burn CPU.
//
// Only signal-safe cross-process liveness check available without
// becoming the process's parent (POSIX waitpid requires being the
// parent). PID reuse over a 10-second window on macOS's >99999 PID
// space is a non-concern for the single caller (Stop's adopted-path
// after a SIGTERM to that same PID).
func waitProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return errors.Is(err, syscall.ESRCH)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
