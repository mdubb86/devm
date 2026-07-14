package main

import (
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
		p.done <- serviceapi.RunService(ctx, serviceapi.Build{
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
		"__LOG_OUT__", filepath.Join(logDir, "com.devm.service.out.log"),
		"__LOG_ERR__", filepath.Join(logDir, "com.devm.service.err.log"),
		"__HOME__", home,
		"__USER__", userName,
	).Replace(serviceapi.LaunchdPlistTemplate)

	prog := &kardianosProgram{}
	cfg := &service.Config{
		Name:        "com.devm.service",
		DisplayName: "devm",
		Description: "devm reverse proxy + DNS + sandbox lifecycle",
		Executable:  exe,
		Arguments:   []string{"serve"},
		Option: service.KeyValue{
			"LaunchdConfig": plistText,
			"UserService":   false, // Force LaunchDaemon path; Status() reads /Library/LaunchDaemons/ from any euid.
		},
	}
	return service.New(prog, cfg)
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
			// background process bound to $DEVM_RUNTIME_DIR/devm.sock.
			// Signal handling: cancel context on SIGTERM/SIGINT so
			// RunService's oklog/run group unwinds cleanly.
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			// Auto-rebuild the base image when this binary's provisioning
			// script diverges from what devm-base was built with. Production
			// `serve` (kardianos path below) runs as launchd and cannot
			// safely shell out to tart for a multi-minute build; that path
			// relies on `devm install` to have already synced the image.
			// Foreground `serve` is the e2e-isolated entry point — running
			// under a user shell with tart available — so it's the right
			// place to catch stale-base-image on branches that changed the
			// provisioning script.
			needs, _, _ := image.NeedsBuild()
			if needs {
				fmt.Fprintln(os.Stderr, "devm serve --foreground: base image out of date; rebuilding devm-base…")
				if err := image.BuildBaseImage(ctx, os.Stderr); err != nil {
					return fmt.Errorf("build base image: %w", err)
				}
			}

			return serviceapi.RunService(ctx, serviceapi.Build{
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
	needs, _, _ := image.NeedsBuild()
	if needs {
		reporter.Step("building devm-base", false)
		if err := image.BuildBaseImage(ctx, logFile); err != nil {
			reporter.Fail()
			tailLog(logPath, 30)
			return fmt.Errorf("base image build failed; see %s", logPath)
		}
	}

	reporter.Info("ready")

	if !sshConfigIncluded(sshconfig.Path()) {
		fmt.Fprintf(os.Stderr,
			"[devm] to enable ssh access to your VMs, add this line to ~/.ssh/config:\n"+
				"    Include \"%s\"\n",
			sshconfig.Path())
	}

	return nil
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

// runPrivilegedInstall composes and runs the install script that
// touches root-owned state (DNS resolver file, CA trust, LaunchDaemon
// plist). Each step is gated on whether it's actually needed:
//
//   - DNS resolver file: skipped when already present with matching bytes
//   - CA trust: skipped when the cert is already in the System Keychain
//   - Daemon plist + bootstrap: skipped when the daemon is already
//     running with a Fingerprint that matches this CLI's
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

	dnsState, err := serviceapi.CheckResolverFile()
	if err != nil {
		return false, fmt.Errorf("check %s: %w", serviceapi.ResolverFilePath, err)
	}
	needsDNS := dnsState != serviceapi.ResolverFileMatches

	trusted, err := serviceapi.CheckCATrusted()
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: could not check CA trust state: %v\n", err)
		trusted = true
	}
	needsCA := !trusted

	needsDaemon := !daemonInSyncWithCLI(ctx)

	if !needsDNS && !needsCA && !needsDaemon {
		return false, nil
	}

	// CA generation is unprivileged; do it before the sudo block so
	// the script has a cert file to point at.
	var rootCertPath string
	if needsCA {
		if _, err := serviceapi.LoadOrGenerate(); err != nil {
			return false, fmt.Errorf("generate CA: %w", err)
		}
		runDir, err := serviceapi.EnsureRuntimeDir()
		if err != nil {
			return false, fmt.Errorf("resolve CA cert path: %w", err)
		}
		rootCertPath = filepath.Join(runDir, "ca", "root.crt")
	}

	var sb strings.Builder
	sb.WriteString("set -e\n")
	if needsDNS {
		if dnsState == serviceapi.ResolverFileDiverged {
			fmt.Printf("note: %s exists but doesn't match — overwriting.\n",
				serviceapi.ResolverFilePath)
		}
		sb.WriteString("mkdir -p /etc/resolver\n")
		fmt.Fprintf(&sb, "cat > %s <<'EOF'\n%sEOF\n",
			serviceapi.ResolverFilePath, serviceapi.CanonicalResolverContents())
	}
	if needsCA {
		fmt.Fprintf(&sb, "security add-trusted-cert -d -r trustRoot -k %s %s\n",
			shellQuote(serviceapi.SystemKeychain), shellQuote(rootCertPath))
	}
	if needsDaemon {
		// Full reload path: unload any running daemon, wipe the plist
		// so kardianos writes a fresh one pointing at THIS binary, then
		// let _kardianos install write + bootstrap. `bootout` and `rm`
		// use `|| true` so a first-time install (no plist, no daemon
		// loaded) still succeeds.
		sb.WriteString("launchctl bootout system/com.devm.service 2>/dev/null || true\n")
		fmt.Fprintf(&sb, "rm -f %s\n", shellQuote(launchdPlistPath))
		fmt.Fprintf(&sb, "%s _kardianos install\n", shellQuote(exe))
	}

	c := exec.CommandContext(ctx, "sudo", "bash", "-c", sb.String())
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
	c := serviceapi.NewClient()
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
		_ = os.Remove(serviceapi.SocketPath())
		// Runtime dir is user-owned (holds the CA key, iron-proxy configs,
		// and the socket parent). Wiping it makes uninstall a clean slate
		// so a subsequent `devm install` regenerates the CA fresh; leaving
		// stale keys around after uninstall would surprise users who ran
		// uninstall to reset a broken setup.
		runtimeDir := filepath.Dir(serviceapi.SocketPath())
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
	script := buildUninstallScript(exe)
	c := exec.Command("sudo", "bash", "-c", script)
	c.Stdout = out
	c.Stderr = out
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		return fmt.Errorf("privileged uninstall: %w", err)
	}
	return nil
}

// buildUninstallScript assembles the privileged cleanup script.
//
// set +e: best-effort each step so partial state still cleans up.
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
func buildUninstallScript(exe string) string {
	var sb strings.Builder
	sb.WriteString("set +e\n")
	sb.WriteString("launchctl bootout system/com.devm.service 2>/dev/null\n")
	sb.WriteString("pkill -TERM -f 'iron-proxy -config .*/iron-proxy/.*\\.yaml' 2>/dev/null\n")
	fmt.Fprintf(&sb, "%s _kardianos uninstall\n", shellQuote(exe))
	fmt.Fprintf(&sb, "rm -f %s\n", shellQuote(serviceapi.ResolverFilePath))
	fmt.Fprintf(&sb, "security delete-certificate -c %s %s 2>/dev/null\n",
		shellQuote(serviceapi.CATrustCertCN), shellQuote(serviceapi.SystemKeychain))
	return sb.String()
}

// sshConfigIncluded reports whether the user's ~/.ssh/config has an
// Include line pointing at path. Missing file → treated as not included.
func sshConfigIncluded(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return false
	}
	needle := `Include "` + path + `"`
	return strings.Contains(string(data), needle)
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

// launchdPlistPath is the system-wide LaunchDaemon plist path.
const launchdPlistPath = "/Library/LaunchDaemons/com.devm.service.plist"

// launchdTarget is the modern launchctl target for our service, used
// with bootstrap/bootout.
const launchdTarget = "system/com.devm.service"

// launchdBootstrap loads the plist via modern `launchctl bootstrap`.
// Replacement for kardianos's Start (`launchctl load` — deprecated on
// modern macOS, fails with "Load failed: 5: Input/output error").
func launchdBootstrap() error {
	out, err := exec.Command("launchctl", "bootstrap", "system", launchdPlistPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap system %s: %v: %s", launchdPlistPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// launchdBootout deregisters + SIGTERMs the service via modern
// `launchctl bootout`. Replacement for kardianos's Stop (`launchctl
// unload` — deprecated, leaves KeepAlive daemons alive).
func launchdBootout() error {
	out, err := exec.Command("launchctl", "bootout", launchdTarget).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootout %s: %v: %s", launchdTarget, err, strings.TrimSpace(string(out)))
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

func init() {
	// --foreground routes `serve` directly through RunService (no
	// kardianos). Used by e2e's isolated mode so a test daemon can
	// run in a private DEVM_RUNTIME_DIR without touching launchd.
	serveCmd.Flags().Bool("foreground", false, "run RunService directly, bypassing kardianos (e2e-isolated mode)")
	rootCmd.AddCommand(serveCmd, installCmd, uninstallCmd)
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
	)
	rootCmd.AddCommand(kardianosCmd)
	// Suppress signal for the long-running serve when run interactively.
	signal.Ignore(syscall.SIGPIPE)
}

