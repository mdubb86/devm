package serviceapi

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/supervisor"
)

// safeMutagenIdent constrains project ids and guest host aliases used
// in generated ssh_config content — the same whitelist
// internal/serviceapi/sshconfig applies to project names, since both
// end up interpolated into an OpenSSH config file (newline, quote, and
// wildcard characters all carry meaning there).
var safeMutagenIdent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

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

// mutagenHomeDir is HOME for devm's mutagen daemon process. mutagen
// v0.18.1 has no flag to point its ssh shell-out at a specific config
// file — it always reads $HOME/.ssh/config — so devm gives the daemon
// a private HOME containing only a devm-managed ssh config tree. The
// user's real ~/.ssh/config is never touched.
func mutagenHomeDir(cfg identity.Config) string {
	return filepath.Join(cfg.RuntimeDir(), "mutagen", "ssh", "home")
}

// mutagenSSHConfigDDir is the Include target directory inside the
// mutagen-private HOME's ssh config.
func mutagenSSHConfigDDir(cfg identity.Config) string {
	return filepath.Join(mutagenHomeDir(cfg), ".ssh", "config.d")
}

// mutagenSSHConfigDPath is the per-project include file
// WriteMutagenSSHConfig writes and RemoveMutagenSSHConfig deletes.
func mutagenSSHConfigDPath(cfg identity.Config, projectID string) string {
	return filepath.Join(mutagenSSHConfigDDir(cfg), projectID+".conf")
}

// SpawnMutagen extracts the embedded mutagen binary, starts its daemon
// with a data directory and HOME scoped under cfg.RuntimeDir(), and
// adopts the resulting PID under supervisor.RoleMutagen.
func SpawnMutagen(ctx context.Context, cfg identity.Config, sup *supervisor.Supervisor) error {
	dataDir := mutagenDataDir(cfg)
	homeDir := mutagenHomeDir(cfg)
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("mutagen: data dir %s: %w", dataDir, err)
	}
	if err := os.MkdirAll(homeDir, 0700); err != nil {
		return fmt.Errorf("mutagen: home dir %s: %w", homeDir, err)
	}

	bin, err := mutagenEnsureFn(cfg.RuntimeDir())
	if err != nil {
		return fmt.Errorf("mutagen: extract binary: %w", err)
	}

	cli := &mutagen.CLI{
		Binary:   bin,
		DataDir:  dataDir,
		ExtraEnv: []string{"HOME=" + homeDir},
	}

	pid, err := mutagenDaemonStartFn(cli)
	if err != nil {
		return fmt.Errorf("mutagen: daemon start: %w", err)
	}

	key := supervisor.Key{Role: supervisor.RoleMutagen}
	sup.Adopt(key, pid)
	log.Printf("mutagen: adopted daemon pid=%d bin=%s data=%s home=%s", pid, bin, dataDir, homeDir)
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

	if sha, err := mutagenBinarySha(cfg); err == nil && sha == mutagen.EmbeddedSha256() {
		sup.Adopt(key, pid)
		log.Printf("mutagen: adopted existing daemon pid=%d data=%s", pid, dataDir)
		return nil
	}

	sup.Adopt(key, pid)
	if err := sup.Stop(ctx, key); err != nil {
		return fmt.Errorf("mutagen: stop stale daemon pid %d: %w", pid, err)
	}
	log.Printf("mutagen: stopped stale daemon pid=%d (binary changed), respawning", pid)
	return SpawnMutagen(ctx, cfg, sup)
}

// StopMutagen stops the mutagen daemon supervised under
// supervisor.RoleMutagen (SIGTERM, 10s grace, then SIGKILL).
func StopMutagen(sup *supervisor.Supervisor) error {
	return sup.Stop(context.Background(), supervisor.Key{Role: supervisor.RoleMutagen})
}

// WriteMutagenSSHConfig writes a per-project ssh_config include at
// <mutagenHomeDir>/.ssh/config.d/<projectID>.conf pointing mutagen's
// ssh shell-out at the guest, and ensures the mutagen-private HOME's
// main ssh config Includes that directory. Idempotent: safe to call
// again for the same or a different project.
func WriteMutagenSSHConfig(cfg identity.Config, projectID, guestHost, guestIP, sshKeyPath, knownHostsPath string) error {
	if !safeMutagenIdent.MatchString(projectID) {
		return fmt.Errorf("mutagen: unsafe project id %q", projectID)
	}
	if !safeMutagenIdent.MatchString(guestHost) {
		return fmt.Errorf("mutagen: unsafe guest host %q", guestHost)
	}

	if err := ensureMutagenMainSSHConfig(cfg); err != nil {
		return err
	}

	body := fmt.Sprintf(`Host %s
  HostName %s
  User devm
  IdentityFile %s
  UserKnownHostsFile %s
  StrictHostKeyChecking yes
  IdentitiesOnly yes
`, guestHost, guestIP, sshKeyPath, knownHostsPath)

	path := mutagenSSHConfigDPath(cfg, projectID)
	if err := writeFileAtomic(path, []byte(body), 0600); err != nil {
		return fmt.Errorf("mutagen: write %s: %w", path, err)
	}
	return nil
}

// RemoveMutagenSSHConfig deletes the per-project ssh_config include
// written by WriteMutagenSSHConfig. No-op if it's already gone (e.g.
// `devm teardown` racing a prior removal).
func RemoveMutagenSSHConfig(cfg identity.Config, projectID string) error {
	if !safeMutagenIdent.MatchString(projectID) {
		return fmt.Errorf("mutagen: unsafe project id %q", projectID)
	}
	path := mutagenSSHConfigDPath(cfg, projectID)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("mutagen: remove %s: %w", path, err)
	}
	return nil
}

// mutagenMainSSHConfigIncludeLine is the directive ensureMutagenMainSSHConfig
// keeps at the top of the mutagen-private HOME's ssh config, so every
// file dropped into config.d/ by WriteMutagenSSHConfig takes effect.
const mutagenMainSSHConfigIncludeLine = "Include config.d/*.conf\n"

// ensureMutagenMainSSHConfig makes sure
// <mutagenHomeDir>/.ssh/config exists, mode 0600, starting with
// mutagenMainSSHConfigIncludeLine. Idempotent: a file that already
// starts with the include line is left untouched.
func ensureMutagenMainSSHConfig(cfg identity.Config) error {
	dir := filepath.Join(mutagenHomeDir(cfg), ".ssh")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mutagen: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "config")

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("mutagen: read %s: %w", path, err)
	}
	if strings.HasPrefix(string(existing), mutagenMainSSHConfigIncludeLine) {
		return nil
	}

	body := mutagenMainSSHConfigIncludeLine + string(existing)
	if err := writeFileAtomic(path, []byte(body), 0600); err != nil {
		return fmt.Errorf("mutagen: write %s: %w", path, err)
	}
	return nil
}

// writeFileAtomic writes data to path via temp-file-then-rename in
// path's own directory, so a crash mid-write never leaves a
// half-written file at path. Creates parent dirs as needed.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}
