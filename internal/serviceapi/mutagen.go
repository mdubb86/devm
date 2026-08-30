package serviceapi

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/supervisor"
)

// mutagenEnsureFn is the test-injection seam for mutagen.Ensure.
var mutagenEnsureFn = mutagen.Ensure

// mutagenDaemonStartFn is the test-injection seam for
// (*mutagen.CLI).DaemonStart — production always delegates to the real
// CLI method, which shells out to `mutagen daemon start` and resolves
// the resulting PID via lsof on the data dir's daemon.lock.
var mutagenDaemonStartFn = func(cli *mutagen.CLI) (int, error) {
	return cli.DaemonStart()
}

// mutagenLockPID resolves the PID currently holding dataDir's
// daemon.lock via lsof, independent of any particular *mutagen.CLI
// instance — AdoptMutagenDaemon needs this before it has decided
// whether the running daemon's binary is even worth reusing. Returns
// (0, nil) when no process holds the lock (the normal "nothing
// running yet" case). Test-injection seam.
var mutagenLockPID = func(dataDir string) (int, error) {
	lockPath := filepath.Join(dataDir, "daemon", "daemon.lock")
	stdout, stderr, code, err := mutagen.OSExec("lsof", []string{"-t", lockPath}, os.Environ())
	if err != nil {
		return 0, fmt.Errorf("lsof %s: %w", lockPath, err)
	}
	if code != 0 {
		_ = stderr // lsof's normal "nothing holds this file" exit; not an error
		return 0, nil
	}
	fields := strings.Fields(stdout)
	if len(fields) == 0 {
		return 0, nil
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("lsof %s: parse pid %q: %w", lockPath, fields[0], err)
	}
	return pid, nil
}

// mutagenBinarySha reads the sidecar sha mutagen.Ensure wrote next to
// the currently-extracted binary. Comparing this against
// mutagen.EmbeddedSha256() tells AdoptMutagenDaemon whether an
// already-running daemon was launched from the binary this build
// ships, or a stale one from before a devm upgrade. Test-injection
// seam.
var mutagenBinarySha = func(cfg identity.Config) (string, error) {
	sidecar := filepath.Join(cfg.RuntimeDir(), "bin", "mutagen.sha256")
	b, err := os.ReadFile(sidecar)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// mutagenSessionsDir is where per-project generated sync config yaml
// files live (see internal/mutagen.ConfigFilePath).
func mutagenSessionsDir(cfg identity.Config) string {
	return filepath.Join(cfg.RuntimeDir(), "mutagen", "sessions")
}

// mutagenDataDir is MUTAGEN_DATA_DIRECTORY for devm's mutagen daemon —
// isolated per identity (prod vs e2e) same as every other runtime-dir
// subtree.
func mutagenDataDir(cfg identity.Config) string {
	return filepath.Join(cfg.RuntimeDir(), "mutagen", "data")
}

// mutagenStopPhaseFn is the test-injection seam for the flush+pause
// step /vm/stop runs (before gracefulStopVM) against the project's
// mutagen sessions. Production always extracts the real embedded
// binary and calls StopPhase; tests substitute a fake to verify
// sequencing without a live mutagen daemon.
var mutagenStopPhaseFn = func(cfg identity.Config, projectID string) error {
	mutagenBin, err := mutagenEnsureFn(cfg.RuntimeDir())
	if err != nil {
		return fmt.Errorf("mutagen: extract binary: %w", err)
	}
	mutagenCLI := &mutagen.CLI{Binary: mutagenBin, DataDir: mutagenDataDir(cfg), Exec: mutagen.OSExec}
	return StopPhase(mutagenCLI, projectID)
}

// SpawnMutagen extracts the embedded mutagen binary, starts its daemon
// with a data directory scoped under cfg.RuntimeDir(), and adopts the
// resulting PID under supervisor.RoleMutagen.
//
// The daemon's outbound sessions transport via MUTAGEN_SSH_PATH, which
// points at the tart-mutagen-ssh shim (see cmd/tart-mutagen-ssh) instead
// of the system ssh client. The shim dispatches through `tart exec`, so
// the daemon has no sshd dependency and never touches ~/.ssh/config.
func SpawnMutagen(ctx context.Context, cfg identity.Config, sup *supervisor.Supervisor) error {
	dataDir := mutagenDataDir(cfg)
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("mutagen: data dir %s: %w", dataDir, err)
	}

	bin, err := mutagenEnsureFn(cfg.RuntimeDir())
	if err != nil {
		return fmt.Errorf("mutagen: extract binary: %w", err)
	}

	cli := &mutagen.CLI{
		Binary:  bin,
		DataDir: dataDir,
		ExtraEnv: []string{
			"MUTAGEN_SSH_PATH=" + MutagenSSHDir(cfg),
		},
	}

	pid, err := mutagenDaemonStartFn(cli)
	if err != nil {
		return fmt.Errorf("mutagen: daemon start: %w", err)
	}

	key := supervisor.Key{Role: supervisor.RoleMutagen}
	sup.Adopt(key, pid)
	log.Printf("mutagen: adopted daemon pid=%d bin=%s data=%s", pid, bin, dataDir)
	return nil
}

// AdoptMutagenDaemon is called on devm daemon start. If a mutagen
// daemon is already running against cfg's data dir, it's adopted as-is
// when its binary matches this build's embedded sidecar; a mismatch
// means devm was upgraded since that daemon started, so it's stopped
// and a fresh one spawned from the current embedded binary. If none is
// running, SpawnMutagen starts one.
func AdoptMutagenDaemon(ctx context.Context, cfg identity.Config, sup *supervisor.Supervisor) error {
	dataDir := mutagenDataDir(cfg)
	pid, err := mutagenLockPID(dataDir)
	if err != nil {
		return fmt.Errorf("mutagen: check running daemon: %w", err)
	}
	if pid == 0 {
		return SpawnMutagen(ctx, cfg, sup)
	}

	key := supervisor.Key{Role: supervisor.RoleMutagen}

	// Always stop-and-respawn any adopted daemon: mutagen sessions persist
	// in DataDir across daemon restarts (mutagen resumes them on next
	// start), and this guarantees the daemon inherits the current build's
	// full env — including MUTAGEN_SSH_PATH (points at the tart-mutagen-ssh
	// shim) and MUTAGEN_DATA_DIRECTORY. A stale env from a previous devm
	// build would silently break the sync transport in ways that only
	// surface at the first sync create.
	sup.Adopt(key, pid)
	if err := sup.Stop(ctx, key); err != nil {
		return fmt.Errorf("mutagen: stop existing daemon pid %d: %w", pid, err)
	}
	log.Printf("mutagen: stopped existing daemon pid=%d, respawning with current env", pid)
	return SpawnMutagen(ctx, cfg, sup)
}

// StopMutagen stops the mutagen daemon supervised under
// supervisor.RoleMutagen (SIGTERM, 10s grace, then SIGKILL).
func StopMutagen(sup *supervisor.Supervisor) error {
	return sup.Stop(context.Background(), supervisor.Key{Role: supervisor.RoleMutagen})
}
