package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kardianos/service"
	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/image"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/serviceapi/sshconfig"
	"github.com/mdubb86/devm/internal/status"
)

// resolveInstallUser returns the username + home directory of the
// person installing devm. Under sudo, $USER is "root" but $SUDO_USER
// holds the real invoker — we prefer that. Refuses to install when
// the resolved user is "root" (means devm install was launched from
// a root shell without sudo, which is the wrong way to do it).
//
// lookup is injectable for testing; pass user.Lookup in production.
func resolveInstallUser(lookup func(string) (*user.User, error)) (name, home string, err error) {
	name = os.Getenv("SUDO_USER")
	if name == "" {
		name = os.Getenv("USER")
	}
	if name == "" || name == "root" {
		return "", "", fmt.Errorf("cannot install as root; run `devm install` as your normal user account")
	}
	if lookup == nil {
		lookup = user.Lookup
	}
	u, err := lookup(name)
	if err != nil {
		return "", "", fmt.Errorf("look up user %q: %w", name, err)
	}
	return u.Username, u.HomeDir, nil
}

// kardianosProgram is the kardianos/service.Interface implementation.
// On Start, we kick off serviceapi.RunService in a goroutine and
// return — kardianos requires Start() to be non-blocking.
type kardianosProgram struct {
	cancel context.CancelFunc
	done   chan error
}

func (p *kardianosProgram) Start(_ service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan error, 1)
	go func() {
		p.done <- serviceapi.RunService(ctx, cfg, serviceapi.Build{
			Version: Version, Commit: Commit, Date: Date, Fingerprint: Fingerprint,
			BinaryPath: resolvedSelfPath(),
		})
	}()
	return nil
}

// resolvedSelfPath returns os.Executable() with symlinks resolved.
// Compared client-side to daemon.BinaryPath (also resolved) to detect
// "rebuild in place" drift: same on-disk file, different fingerprint
// (== new bytes). Returns "" on any error — callers treat missing as
// "unknown, don't auto-heal".
func resolvedSelfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe
	}
	return real
}

func (p *kardianosProgram) Stop(_ service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	// Wait briefly for RunService to drain.
	if p.done != nil {
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
		}
	}
	return nil
}

// newKardianosService builds the kardianos service descriptor.
// Same identity used by all install/uninstall/lifecycle commands.
func newKardianosService() (service.Service, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}

	userName, home, err := resolveInstallUser(nil)
	if err != nil {
		return nil, err
	}
	logDir := filepath.Join(home, "Library", "Logs")

	plistText := strings.NewReplacer(
		"__LOG_OUT__", filepath.Join(logDir, cfg.LaunchdLabelDaemon()+".out.log"),
		"__LOG_ERR__", filepath.Join(logDir, cfg.LaunchdLabelDaemon()+".err.log"),
		"__HOME__", home,
		"__USER__", userName,
	).Replace(serviceapi.LaunchdPlistTemplate)

	prog := &kardianosProgram{}
	// Named svcCfg (not cfg) — kardianos's service.Config, distinct
	// from the package-level identity.Config cfg this function reads
	// from above.
	svcCfg := &service.Config{
		Name:        cfg.LaunchdLabelDaemon(),
		DisplayName: "devm",
		Description: "devm reverse proxy + DNS + sandbox lifecycle",
		Executable:  exe,
		Arguments:   []string{"serve"},
		Option: service.KeyValue{
			"LaunchdConfig": plistText,
			"UserService":   false, // Force LaunchDaemon path; Status() reads /Library/LaunchDaemons/ from any euid.
		},
	}
	return service.New(prog, svcCfg)
}

var serveCmd = &cobra.Command{
	Use:    "serve",
	Short:  "Run the devm service (invoked by launchd)",
	Hidden: true, // not user-facing; launchd calls this
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		foreground, _ := cmd.Flags().GetBool("foreground")
		if foreground {
			// Bypass kardianos and run RunService directly. Kardianos'
			// svc.Run() on Darwin expects to be launched by launchd
			// and exits silently when invoked from a normal shell — no
			// use to us in e2e-isolated mode where we want a plain
			// background process bound to cfg.SocketPath().
			// Signal handling: cancel context on SIGTERM/SIGINT so
			// RunService's oklog/run group unwinds cleanly.
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return serviceapi.RunService(ctx, cfg, serviceapi.Build{
				Version: Version, Commit: Commit, Date: Date, Fingerprint: Fingerprint,
				BinaryPath: resolvedSelfPath(),
			})
		}
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		return svc.Run()
	},
}

// buildBaseIfNeededCmd rebuilds devm-base when its provisioning-script
// hash has drifted (or the image is missing). Hidden: not part of the
// user surface — called by the e2e harness (`e2e/scripts/run.sh`) so
// isolated runs auto-heal after any branch that changes
// image/provision-base.sh, without needing `devm install`'s sudo path.
//
// Safe to invoke anywhere tart is on PATH: touches only tart's local
// image cache. No LaunchDaemon, no DNS, no CA writes.
var buildBaseIfNeededCmd = &cobra.Command{
	Use:    "_build-base-if-needed",
	Short:  "[internal] Rebuild devm-base when the provisioning script has drifted",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		if _, err := exec.LookPath("tart"); err != nil {
			return fmt.Errorf("tart not found on PATH: %w", err)
		}
		needs, _, err := image.NeedsBuild(cfg)
		if err != nil {
			return fmt.Errorf("check base image state: %w", err)
		}
		if !needs {
			return nil
		}
		fmt.Fprintf(os.Stderr, "devm _build-base-if-needed: base image drifted; rebuilding %s…\n", cfg.BaseImageName())
		return image.BuildBaseImage(cmd.Context(), cfg, os.Stderr)
	},
}

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Register devm as a user-level launchd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return runInstallFlow(cmd.Context())
	},
}

// runInstallFlow is the shared install pipeline used by `devm install`
// directly and by `devm upgrade` after the binary swap. It checks
// tart is present, runs the (conditionally-sudo) install script,
// rebuilds the base image if needed, and reports "ready" or "already
// up to date". Idempotent on every step — a second call in-sync is
// silent and does not fire a Touch ID prompt.
func runInstallFlow(ctx context.Context) error {
	if _, err := exec.LookPath("tart"); err != nil {
		return fmt.Errorf("tart not found on PATH. Install it first:\n\n  brew install cirruslabs/cli/tart\n")
	}

	reporter := status.New(os.Stderr)
	defer reporter.Stop()

	logPath, logFile, err := openInstallLog()
	if err != nil {
		return err
	}
	defer logFile.Close()

	// Privileged install: silent. The sudo prompt itself (if it
	// fires) is the user-visible activity. If everything is
	// already in place, no prompt — and no log noise.
	didWork, err := runPrivilegedInstall(ctx, logFile)
	if err != nil {
		tailLog(logPath, 30)
		return fmt.Errorf("privileged install failed; see %s", logPath)
	}
	if !didWork {
		reporter.Info("already up to date")
		return nil
	}

	// Base image: long-running, no terminal output (captured to
	// log). Spinner has the terminal to itself. The provisioning
	// script is embedded in the binary via //go:embed, so no
	// on-disk image/ directory is needed.
	needs, _, _ := image.NeedsBuild(cfg)
	if needs {
		reporter.Step("building "+cfg.BaseImageName(), false)
		if err := image.BuildBaseImage(ctx, cfg, logFile); err != nil {
			reporter.Fail()
			tailLog(logPath, 30)
			return fmt.Errorf("base image build failed; see %s", logPath)
		}
	}

	reporter.Info("ready")

	before, _ := os.ReadFile(userSSHConfigPathForLog())
	if err := sshconfig.EnsureInclude(cfg); err != nil {
		return fmt.Errorf("update ~/.ssh/config: %w", err)
	}
	after, _ := os.ReadFile(userSSHConfigPathForLog())
	if !bytes.Equal(before, after) {
		fmt.Fprintf(os.Stderr, "[devm] added ssh access include line to ~/.ssh/config\n")
	}

	return nil
}

// userSSHConfigPathForLog returns ~/.ssh/config purely so runInstallFlow
// can diff before/after content and report whether EnsureInclude
// actually wrote anything. Best-effort: an empty string (home dir
// lookup failure) just makes the before/after comparison a no-op diff,
// which is fine — sshconfig.EnsureInclude itself already surfaced any
// real error.
func userSSHConfigPathForLog() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

// openInstallLog opens ~/Library/Logs/devm/install.log for append. The
// install/uninstall flows redirect verbose subprocess output here so
// the user only sees clean step lines on success; on error, tailLog
// surfaces the tail for diagnosis.
func openInstallLog() (string, *os.File, error) {
	_, home, err := resolveInstallUser(nil)
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(home, "Library", "Logs", "devm")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "install.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return "", nil, fmt.Errorf("open %s: %w", path, err)
	}
	fmt.Fprintf(f, "\n=== %s install log ===\n", time.Now().Format(time.RFC3339))
	return path, f, nil
}

// tailLog prints the last n lines of path to stderr, prefixed so the
// user can tell apart the captured noise from devm's own messages.
func tailLog(path string, n int) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "(could not read log %s: %v)\n", path, err)
		return
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	fmt.Fprintf(os.Stderr, "--- last %d lines of %s ---\n", n, path)
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, l)
	}
	fmt.Fprintln(os.Stderr, "---")
}

// installInputs holds every already-computed decision for the
// privileged install script: which steps are needed and the values
// they act on. buildInstallScript is a pure function over this
// struct — no filesystem or process access — so the assembled script
// can be golden-tested without root or a real install.
type installInputs struct {
	// DevmExe is the CLI binary path baked into the LaunchDaemon plist
	// by `_kardianos install`.
	DevmExe string
	// HelperExe is the resolved devm-helper binary path the helper's
	// LaunchDaemon plist should point at directly (sibling of DevmExe
	// — no system-path copy, same pattern kardianos uses for the main
	// daemon). Empty skips the entire helper block (group creation,
	// plist install, bootstrap).
	HelperExe string
	// HelperLogDir is the installing user's ~/Library/Logs, used to
	// build the helper plist's StandardOutPath/StandardErrorPath.
	// Ignored when HelperExe is empty.
	HelperLogDir string
	// InstallUser is added to the group's membership list.
	// Ignored when HelperExe is empty and NeedsGroup is false.
	InstallUser string

	NeedsDNS         bool
	ResolverContents string

	NeedsCA      bool
	RootCertPath string

	NeedsDaemon bool

	// NeedsAliases is true when at least one lo0 loopback alias in
	// cfg's pool (127.42.0.<PoolStart>..<PoolEnd>) is missing.
	NeedsAliases bool

	// NeedsGroup is true when cfg.GroupName() doesn't exist yet in
	// Directory Services.
	NeedsGroup bool
}

// buildInstallScript assembles the privileged install script from
// already-computed inputs. Order: DNS resolver file, CA trust,
// helper (group + binary + plist + bootstrap), then the main
// daemon reload — matching runPrivilegedInstall's prior inline
// ordering plus the new helper step inserted before the daemon
// reload.
func buildInstallScript(inputs installInputs) string {
	var sb strings.Builder
	sb.WriteString("set -e\n")

	if inputs.NeedsDNS {
		sb.WriteString("mkdir -p /etc/resolver\n")
		fmt.Fprintf(&sb, "cat > %s <<'EOF'\n%sEOF\n",
			cfg.ResolverFilePath, inputs.ResolverContents)
	}
	if inputs.NeedsCA {
		fmt.Fprintf(&sb, "security add-trusted-cert -d -r trustRoot -k %s %s\n",
			shellQuote(serviceapi.SystemKeychain), shellQuote(inputs.RootCertPath))
	}
	// Group block runs whenever the group itself might be missing, or
	// we're (re)installing the helper (which needs InstallUser in it).
	if inputs.NeedsGroup || inputs.HelperExe != "" {
		// Create the group idempotently (safe re-run at every install).
		// GID 802: PrimaryGroupID collision fallback — pick your own if
		// this collides with an existing group on your system (org
		// management / MDM tools sometimes claim GIDs in the 300-999
		// system range, and `dscl . -create` aborts under `set -e` if
		// the GID is already taken).
		group := cfg.GroupName()
		fmt.Fprintf(&sb, "dscl . -read /Groups/%s >/dev/null 2>&1 || dscl . -create /Groups/%s PrimaryGroupID 802\n", group, group)
		if inputs.InstallUser != "" {
			fmt.Fprintf(&sb, "dscl . -read /Groups/%s GroupMembership 2>/dev/null | grep -qw %s || dscl . -append /Groups/%s GroupMembership %s\n",
				group, inputs.InstallUser, group, inputs.InstallUser)
		}
	}
	if inputs.HelperExe != "" {
		// No system-path copy: the plist points directly at the
		// resolved helper binary sitting alongside DevmExe, same
		// pattern kardianos uses for the main daemon (Executable: exe).
		fmt.Fprintf(&sb, "install -m 0644 /dev/stdin %s <<'EOF'\n%sEOF\n",
			cfg.LaunchdPlistHelper(), helperPlistContent(inputs.HelperExe, inputs.HelperLogDir))
		sb.WriteString("launchctl bootout " + cfg.LaunchdTargetHelper() + " 2>/dev/null || true\n")
		// Bootstrap via our own retry-capable helper (not a raw
		// `launchctl bootstrap` line) — see launchdBootstrapPlist:
		// launchd can transiently EIO here right after the bootout
		// above, and a bare shell invocation has no way to retry.
		fmt.Fprintf(&sb, "%s _kardianos bootstrap-helper\n", shellQuote(inputs.DevmExe))
	}
	if inputs.NeedsAliases {
		for n := cfg.PoolStart; n <= cfg.PoolEnd; n++ {
			fmt.Fprintf(&sb, "/sbin/ifconfig lo0 alias 127.42.0.%d 2>/dev/null || true\n", n)
		}
	}
	if inputs.NeedsDaemon {
		// Full reload path: unload any running daemon, wipe the plist
		// so kardianos writes a fresh one pointing at THIS binary, then
		// let _kardianos install write + bootstrap. `bootout` and `rm`
		// use `|| true` so a first-time install (no plist, no daemon
		// loaded) still succeeds.
		sb.WriteString("launchctl bootout " + cfg.LaunchdTargetDaemon() + " 2>/dev/null || true\n")
		fmt.Fprintf(&sb, "rm -f %s\n", shellQuote(cfg.LaunchdPlistDaemon()))
		fmt.Fprintf(&sb, "%s _kardianos install\n", shellQuote(inputs.DevmExe))
	}

	return sb.String()
}

// helperNeedsInstall reports whether the helper's LaunchDaemon plist
// needs to be (re)written: missing entirely, pointing at a stale
// program path (the devm binary moved since the last install), or
// piggybacked on a daemon reinstall (the two binaries are built
// together by `just build`, so a CLI/daemon fingerprint mismatch means
// helper is likely stale too).
func helperNeedsInstall(needsDaemon bool, programPath string) bool {
	if needsDaemon {
		return true
	}
	data, err := os.ReadFile(cfg.LaunchdPlistHelper())
	if err != nil {
		return true
	}
	return !strings.Contains(string(data), programPath)
}

// helperSourcePath returns the devm-helper binary expected to sit
// alongside devmExe — the layout `just build` produces in bin/. The
// helper's LaunchDaemon plist points directly at this path (no
// system-path copy), so a CLI built as "devm-e2e" resolves its own
// "devm-e2e-helper" sibling rather than colliding with prod's.
func helperSourcePath(devmExe string) string {
	return filepath.Join(filepath.Dir(devmExe), filepath.Base(devmExe)+"-helper")
}

// aliasesNeedInstall reports whether at least one lo0 loopback alias
// in cfg's per-project pool (127.42.0.<PoolStart>..<PoolEnd>) is
// missing. ifconfig alias creation is idempotent (the install script
// guards each with `|| true`), so on true the script just (re)asserts
// every alias in range rather than diffing individually.
func aliasesNeedInstall(cfg identity.Config) bool {
	out, err := exec.Command("ifconfig", "lo0").Output()
	if err != nil {
		return true
	}
	text := string(out)
	for n := cfg.PoolStart; n <= cfg.PoolEnd; n++ {
		if !strings.Contains(text, fmt.Sprintf("127.42.0.%d ", n)) {
			return true
		}
	}
	return false
}

// groupExists reports whether cfg.GroupName() already exists in
// Directory Services (`dscl . -read /Groups/<name>` exits 0).
func groupExists(name string) bool {
	return exec.Command("dscl", ".", "-read", "/Groups/"+name).Run() == nil
}

// runPrivilegedInstall composes and runs the install script that
// touches root-owned state (DNS resolver file, CA trust, LaunchDaemon
// plists, the group, the lo0 alias pool). Each step is gated on
// whether it's actually needed:
//
//   - DNS resolver file: skipped when already present with matching bytes
//   - CA trust: skipped when the cert is already in the System Keychain
//   - Daemon plist + bootstrap: skipped when the daemon is already
//     running with a Fingerprint that matches this CLI's
//   - Helper: skipped when its plist is already present and points at
//     the current helper binary, and the daemon doesn't need reinstalling
//   - Aliases: skipped when every lo0 alias in the pool already exists
//   - Group: skipped when cfg.GroupName() already exists
//
// When every step is a no-op, the function returns without escalating
// to sudo at all — no Touch ID prompt for `devm install` when nothing
// needs installing. Only drift or missing pieces trigger the prompt.
//
// Returns didWork = true when the script actually ran, so the caller
// can distinguish "installed something" from "already up to date" for
// the top-line message.
func runPrivilegedInstall(ctx context.Context, out io.Writer) (didWork bool, err error) {
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("locate executable: %w", err)
	}

	dnsState, err := serviceapi.CheckResolverFile(cfg)
	if err != nil {
		return false, fmt.Errorf("check %s: %w", cfg.ResolverFilePath, err)
	}
	needsDNS := dnsState != serviceapi.ResolverFileMatches

	trusted, err := serviceapi.CheckCATrusted(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: could not check CA trust state: %v\n", err)
		trusted = true
	}
	needsCA := !trusted

	needsDaemon := !daemonInSyncWithCLI(ctx)
	helperProgramPath := helperSourcePath(exe)
	needsHelper := helperNeedsInstall(needsDaemon, helperProgramPath)
	needsAliases := aliasesNeedInstall(cfg)
	needsGroup := !groupExists(cfg.GroupName())

	if !needsDNS && !needsCA && !needsDaemon && !needsHelper && !needsAliases && !needsGroup {
		return false, nil
	}

	// resolveInstallUser is only needed by the group and helper steps
	// below; skip it (and tolerate its error) otherwise so that, e.g.,
	// a `devm install` run as literal root with nothing to install
	// stays a no-op rather than hard-failing on user lookup.
	var installUser, home string
	if needsGroup || needsHelper {
		installUser, home, err = resolveInstallUser(nil)
		if err != nil {
			return false, err
		}
	}

	// CA generation is unprivileged; do it before the sudo block so
	// the script has a cert file to point at.
	var rootCertPath string
	if needsCA {
		if _, err := serviceapi.LoadOrGenerate(cfg); err != nil {
			return false, fmt.Errorf("generate CA: %w", err)
		}
		runDir, err := serviceapi.EnsureRuntimeDir(cfg)
		if err != nil {
			return false, fmt.Errorf("resolve CA cert path: %w", err)
		}
		rootCertPath = filepath.Join(runDir, "ca", "root.crt")
	}

	if needsDNS && dnsState == serviceapi.ResolverFileDiverged {
		fmt.Printf("note: %s exists but doesn't match — overwriting.\n",
			cfg.ResolverFilePath)
	}

	inputs := installInputs{
		DevmExe:          exe,
		NeedsDNS:         needsDNS,
		ResolverContents: cfg.CanonicalResolverContents(),
		NeedsCA:          needsCA,
		RootCertPath:     rootCertPath,
		NeedsDaemon:      needsDaemon,
		NeedsAliases:     needsAliases,
		NeedsGroup:       needsGroup,
		InstallUser:      installUser,
	}
	if needsHelper {
		if _, statErr := os.Stat(helperProgramPath); statErr == nil {
			inputs.HelperExe = helperProgramPath
			inputs.HelperLogDir = filepath.Join(home, "Library", "Logs")
		} else {
			fmt.Fprintf(os.Stderr,
				"note: devm-helper binary not found at %s; skipping network-isolation helper install\n", helperProgramPath)
		}
	}

	c := exec.CommandContext(ctx, "sudo", "bash", "-c", buildInstallScript(inputs))
	c.Stdout = out
	c.Stderr = out
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		return true, fmt.Errorf("privileged install: %w", err)
	}
	return true, nil
}

// daemonInSyncWithCLI reports whether the running daemon shares this
// CLI's Fingerprint — i.e. was built from the same binary. False when
// the daemon is unreachable, doesn't report a Fingerprint, or reports
// a different one. Used by `devm install` to skip the sudo escalation
// when there's genuinely nothing to install.
func daemonInSyncWithCLI(ctx context.Context) bool {
	c := serviceapi.NewClient(cfg)
	if !c.Available(ctx) {
		return false
	}
	b, err := c.BuildInfo(ctx)
	if err != nil {
		return false
	}
	return b.Fingerprint != "" && Fingerprint != "" && b.Fingerprint == Fingerprint
}

// shellQuote wraps s in single quotes, escaping embedded quotes so
// the value survives `sh -c`. Sufficient for absolute filesystem
// paths and our fixed identifiers — no fancy chars expected.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Deregister the devm launchd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		reporter := status.New(os.Stderr)
		defer reporter.Stop()

		logPath, logFile, err := openInstallLog()
		if err != nil {
			return err
		}
		defer logFile.Close()

		if err := runPrivilegedUninstall(logFile); err != nil {
			tailLog(logPath, 30)
			return fmt.Errorf("privileged uninstall failed; see %s", logPath)
		}

		before, _ := os.ReadFile(userSSHConfigPathForLog())
		if err := sshconfig.RemoveInclude(cfg); err != nil {
			return fmt.Errorf("update ~/.ssh/config: %w", err)
		}
		after, _ := os.ReadFile(userSSHConfigPathForLog())
		if !bytes.Equal(before, after) {
			fmt.Fprintf(os.Stderr, "[devm] removed ssh access include line from ~/.ssh/config\n")
		}

		_ = os.Remove(cfg.SocketPath())
		// Runtime dir is user-owned (holds the CA key, iron-proxy configs,
		// and the socket parent). Wiping it makes uninstall a clean slate
		// so a subsequent `devm install` regenerates the CA fresh; leaving
		// stale keys around after uninstall would surprise users who ran
		// uninstall to reset a broken setup.
		runtimeDir := filepath.Dir(cfg.SocketPath())
		if err := os.RemoveAll(runtimeDir); err != nil {
			return fmt.Errorf("remove runtime dir %s: %w", runtimeDir, err)
		}
		reporter.Info("uninstalled")
		return nil
	},
}

func runPrivilegedUninstall(out io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	script := buildUninstallScript(cfg, exe)
	c := exec.Command("sudo", "bash", "-c", script)
	c.Stdout = out
	c.Stderr = out
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		return fmt.Errorf("privileged uninstall: %w", err)
	}
	// tart images live in the invoking user's ~/.tart/, so `tart
	// delete` must run as the user, not as root inside the sudo
	// block above (which would look in /var/root/.tart/ and silently
	// find nothing). Best-effort — a stale base image doesn't block
	// uninstall itself.
	if cfg.DeleteBaseImageOnUninstall {
		td := exec.Command("tart", "delete", cfg.BaseImageName())
		td.Stdout = out
		td.Stderr = out
		_ = td.Run()
	}
	return nil
}

// buildUninstallScript assembles the privileged cleanup script.
//
// set +e: best-effort each step so partial state still cleans up.
//
// The main daemon is booted out before helper: the daemon holds
// FDs it received from helper over SCM_RIGHTS for each bound
// project port, and tearing the daemon down first releases those
// before helper itself goes away.
//
// bootout deregisters + SIGTERMs the daemon synchronously. Must run
// before kardianos's Stop, which uses `launchctl unload` — a legacy
// subcommand that leaves KeepAlive daemons alive
// (github.com/kardianos/service#144).
//
// After the daemon is gone, kill any iron-proxy children it had
// running. They setsid on spawn so they survive daemon death by design
// (see runner.go's AdoptIronProxies); nothing cleans them up on
// uninstall unless we do it here. Match the daemon's own argv pattern
// so the pkill can't hit unrelated processes.
//
// Loopback aliases (one per concurrent project in cfg's pool — see
// the per-project bind isolation design) are torn down best-effort;
// a reboot clears them regardless. cfg's group is removed too — it
// exists solely to grant devm-helper's GID access and is recreated
// idempotently on the next install.
//
// The pkill pattern is anchored on cfg.RuntimeDir() rather than a
// shared "iron-proxy/*.yaml" glob: prod's pattern matches
// .../devm/iron-proxy/, e2e's matches .../devm-e2e/iron-proxy/ —
// disjoint, so `devm-e2e uninstall` can't reap the user's real
// iron-proxy children (and vice versa).
//
// The base image deletion gated on cfg.DeleteBaseImageOnUninstall
// (spec §8.3) runs OUTSIDE this script — see runPrivilegedUninstall.
// tart images live in the invoking user's ~/.tart/, so `tart delete`
// must run as the user, not inside this root-scoped block.
func buildUninstallScript(cfg identity.Config, exe string) string {
	var sb strings.Builder
	sb.WriteString("set +e\n")
	sb.WriteString("launchctl bootout " + cfg.LaunchdTargetDaemon() + " 2>/dev/null\n")
	sb.WriteString("launchctl bootout " + cfg.LaunchdTargetHelper() + " 2>/dev/null || true\n")
	fmt.Fprintf(&sb, "pkill -TERM -f %s 2>/dev/null\n",
		shellQuote(filepath.Join(cfg.RuntimeDir(), "iron-proxy")+"/"))
	fmt.Fprintf(&sb, "%s _kardianos uninstall\n", shellQuote(exe))
	fmt.Fprintf(&sb, "rm -f %s\n", shellQuote(cfg.ResolverFilePath))
	fmt.Fprintf(&sb, "security delete-certificate -c %s %s 2>/dev/null\n",
		shellQuote(cfg.CACommonName()), shellQuote(serviceapi.SystemKeychain))

	for n := cfg.PoolStart; n <= cfg.PoolEnd; n++ {
		fmt.Fprintf(&sb, "/sbin/ifconfig lo0 -alias 127.42.0.%d 2>/dev/null || true\n", n)
	}
	sb.WriteString("rm -f " + cfg.LaunchdPlistHelper() + "\n")
	fmt.Fprintf(&sb, "dscl . -delete /Groups/%s 2>/dev/null || true\n", cfg.GroupName())
	return sb.String()
}

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage the devm background service",
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the devm service is running",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		st, err := svc.Status()
		if err != nil {
			fmt.Println("not installed")
			return nil
		}
		switch st {
		case service.StatusRunning:
			fmt.Println("running")
		case service.StatusStopped:
			fmt.Println("stopped")
		case service.StatusUnknown:
			fmt.Println("not installed")
		}
		return nil
	},
}

// runKardianosUnderSudo shells out to `sudo devm _kardianos <verb>`
// as a single child process — one Touch ID prompt per call.
func runKardianosUnderSudo(verb string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	c := exec.Command("sudo", exe, "_kardianos", verb)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}

var serviceStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the devm service (sudo internal)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return runKardianosUnderSudo("start")
	},
}

var serviceStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the devm service (sudo internal)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return runKardianosUnderSudo("stop")
	},
}

var serviceRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the devm service (sudo internal)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return runKardianosUnderSudo("restart")
	},
}

var kardianosCmd = &cobra.Command{
	Use:    "_kardianos",
	Short:  "Internal kardianos adapters (not user-facing)",
	Hidden: true,
}

// launchctlRunner abstracts a single `launchctl <args...>` invocation
// (combined stdout+stderr, plus the process error — the same contract
// as exec.Cmd.CombinedOutput). Injectable for testing retry behavior
// without spawning real launchctl; nil means "use the real binary".
//
// Same shape as resolveInstallUser's injectable lookup func, for the
// same reason: lets tests drive the retry loop deterministically.
type launchctlRunner func(args ...string) (output string, err error)

// runLaunchctl is the production launchctlRunner.
func runLaunchctl(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// launchdBootstrapBackoff is the wait before each retry attempt (the
// first attempt is immediate). Three attempts total.
var launchdBootstrapBackoff = []time.Duration{0, 500 * time.Millisecond, 1500 * time.Millisecond}

// isTransientLaunchdError reports whether launchctl's combined output
// indicates the transient EIO race rather than a real failure.
// Observed reliably in the window right after `launchctl bootout` +
// `rm` of a label's plist: launchd's internal registration cleanup
// can lag the shell command's exit, so an immediate `bootstrap` on
// the same label returns "Bootstrap failed: 5: Input/output error"
// even though nothing is actually wrong. A retry a moment later
// invariably succeeds with no code change in between.
func isTransientLaunchdError(output string) bool {
	return strings.Contains(output, "5: Input/output error") ||
		strings.Contains(output, "Bootstrap failed: 5")
}

// launchdBootstrapPlist loads plist via `launchctl bootstrap system`,
// retrying on isTransientLaunchdError per launchdBootstrapBackoff.
// Non-transient errors return immediately without retrying. run is
// injectable for testing; nil uses runLaunchctl.
func launchdBootstrapPlist(plist string, run launchctlRunner) error {
	if run == nil {
		run = runLaunchctl
	}
	var lastErr error
	for i, backoff := range launchdBootstrapBackoff {
		if i > 0 {
			time.Sleep(backoff)
		}
		outStr, err := run("bootstrap", "system", plist)
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("launchctl bootstrap system %s: %v: %s", plist, err, outStr)
		if !isTransientLaunchdError(outStr) {
			return lastErr
		}
	}
	return fmt.Errorf("launchctl bootstrap failed after retries: %w", lastErr)
}

// launchdBootstrap loads the daemon's plist via modern `launchctl
// bootstrap`. Replacement for kardianos's Start (`launchctl load` —
// deprecated on modern macOS, fails with "Load failed: 5:
// Input/output error").
func launchdBootstrap() error {
	return launchdBootstrapPlist(cfg.LaunchdPlistDaemon(), nil)
}

// launchdBootout deregisters + SIGTERMs the service via modern
// `launchctl bootout`. Replacement for kardianos's Stop (`launchctl
// unload` — deprecated, leaves KeepAlive daemons alive).
func launchdBootout() error {
	target := cfg.LaunchdTargetDaemon()
	out, err := exec.Command("launchctl", "bootout", target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootout %s: %v: %s", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

var kardianosInstallCmd = &cobra.Command{
	Use:    "install",
	Short:  "[internal] kardianos svc.Install() under sudo",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		// Called only from runPrivilegedInstall's sudo script, which
		// has already bootout'd any prior daemon and removed the plist.
		// Install (writes plist) + bootstrap (loads it) unconditionally.
		if err := svc.Install(); err != nil {
			return fmt.Errorf("svc.Install: %w", err)
		}
		return launchdBootstrap()
	},
}

var kardianosUninstallCmd = &cobra.Command{
	Use:    "uninstall",
	Short:  "[internal] kardianos svc.Uninstall() under sudo",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		st, _ := svc.Status()
		if st == service.StatusUnknown {
			return nil
		}
		_ = launchdBootout()
		return svc.Uninstall()
	},
}

var kardianosStartCmd = &cobra.Command{
	Use:    "start",
	Short:  "[internal] launchctl bootstrap under sudo",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return launchdBootstrap()
	},
}

var kardianosStopCmd = &cobra.Command{
	Use:    "stop",
	Short:  "[internal] launchctl bootout under sudo",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return launchdBootout()
	},
}

var kardianosRestartCmd = &cobra.Command{
	Use:    "restart",
	Short:  "[internal] launchctl bootout+bootstrap under sudo",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		// bootout may error if the service isn't loaded — that's fine,
		// bootstrap will bring it up either way.
		_ = launchdBootout()
		return launchdBootstrap()
	},
}

// kardianosBootstrapHelperCmd loads the devm-helper's plist via
// launchdBootstrapPlist (retry-capable). Invoked by buildInstallScript
// from inside the sudo'd install script, right after that script has
// bootout'd any prior helper — the same transient-EIO window
// launchdBootstrap guards against for the main daemon, so the helper
// gets the same retry rather than a bare `launchctl bootstrap` line
// with no way to recover from it.
var kardianosBootstrapHelperCmd = &cobra.Command{
	Use:    "bootstrap-helper",
	Short:  "[internal] launchctl bootstrap for devm-helper under sudo",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		return launchdBootstrapPlist(cfg.LaunchdPlistHelper(), nil)
	},
}

func init() {
	// --foreground routes `serve` directly through RunService (no
	// kardianos). Used by e2e's isolated mode so a test daemon can
	// run under the e2e identity's own runtime dir without touching
	// launchd.
	serveCmd.Flags().Bool("foreground", false, "run RunService directly, bypassing kardianos (e2e-isolated mode)")
	rootCmd.AddCommand(serveCmd, installCmd, uninstallCmd, buildBaseIfNeededCmd)
	serviceCmd.AddCommand(
		serviceStatusCmd,
		serviceStartCmd,
		serviceStopCmd,
		serviceRestartCmd,
	)
	rootCmd.AddCommand(serviceCmd)
	kardianosCmd.AddCommand(
		kardianosInstallCmd,
		kardianosUninstallCmd,
		kardianosStartCmd,
		kardianosStopCmd,
		kardianosRestartCmd,
		kardianosBootstrapHelperCmd,
	)
	rootCmd.AddCommand(kardianosCmd)
	// Suppress signal for the long-running serve when run interactively.
	signal.Ignore(syscall.SIGPIPE)
}
