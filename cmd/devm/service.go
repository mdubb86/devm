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
		})
	}()
	return nil
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
		if err := runPrivilegedInstall(logFile); err != nil {
			tailLog(logPath, 30)
			return fmt.Errorf("privileged install failed; see %s", logPath)
		}

		// Base image: long-running, no terminal output (captured to
		// log). Spinner has the terminal to itself. The provisioning
		// script is embedded in the binary via //go:embed, so no
		// on-disk image/ directory is needed.
		needs, _, _ := image.NeedsBuild()
		if needs {
			reporter.Step("building devm-base", false)
			if err := image.BuildBaseImage(cmd.Context(), logFile); err != nil {
				reporter.Fail()
				tailLog(logPath, 30)
				return fmt.Errorf("base image build failed; see %s", logPath)
			}
		}

		reporter.Info("ready")
		return nil
	},
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

func runPrivilegedInstall(out io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}

	dnsState, err := serviceapi.CheckResolverFile()
	if err != nil {
		return fmt.Errorf("check %s: %w", serviceapi.ResolverFilePath, err)
	}
	needsDNS := dnsState != serviceapi.ResolverFileMatches

	trusted, err := serviceapi.CheckCATrusted()
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: could not check CA trust state: %v\n", err)
		trusted = true
	}
	needsCA := !trusted

	// CA generation is unprivileged; do it before the sudo block so
	// the script has a cert file to point at.
	var rootCertPath string
	if needsCA {
		if _, err := serviceapi.LoadOrGenerate(); err != nil {
			return fmt.Errorf("generate CA: %w", err)
		}
		runDir, err := serviceapi.EnsureRuntimeDir()
		if err != nil {
			return fmt.Errorf("resolve CA cert path: %w", err)
		}
		rootCertPath = filepath.Join(runDir, "ca", "root.crt")
	}

	// Compose the single sudo script. Ship 4.2 always runs the
	// _kardianos install step so the LaunchDaemon plist + launchctl
	// bootstrap happen under root. DNS + CA steps are conditional.
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
	// Always run the kardianos install — it's idempotent enough at
	// the kardianos level (writes plist, bootstraps; second run with
	// the same content is a no-op aside from the bootstrap re-load).
	fmt.Fprintf(&sb, "%s _kardianos install\n", shellQuote(exe))

	c := exec.Command("sudo", "bash", "-c", sb.String())
	c.Stdout = out
	c.Stderr = out
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		return fmt.Errorf("privileged install: %w", err)
	}
	return nil
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

	// set +e: best-effort each step so partial state still cleans up.
	//
	// bootout deregisters + SIGTERMs the daemon synchronously. Must
	// run before kardianos's Stop, which uses `launchctl unload` — a
	// legacy subcommand that leaves KeepAlive daemons alive
	// (github.com/kardianos/service#144).
	var sb strings.Builder
	sb.WriteString("set +e\n")
	sb.WriteString("launchctl bootout system/com.devm.service 2>/dev/null\n")
	fmt.Fprintf(&sb, "%s _kardianos uninstall\n", shellQuote(exe))
	fmt.Fprintf(&sb, "rm -f %s\n", shellQuote(serviceapi.ResolverFilePath))
	fmt.Fprintf(&sb, "security delete-certificate -c %s %s 2>/dev/null\n",
		shellQuote(serviceapi.CATrustCertCN), shellQuote(serviceapi.SystemKeychain))

	c := exec.Command("sudo", "bash", "-c", sb.String())
	c.Stdout = out
	c.Stderr = out
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		return fmt.Errorf("privileged uninstall: %w", err)
	}
	return nil
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
		st, _ := svc.Status()
		if st == service.StatusUnknown {
			if err := svc.Install(); err != nil {
				return err
			}
		}
		if st != service.StatusRunning {
			return svc.Start()
		}
		return nil
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
		_ = svc.Stop()
		return svc.Uninstall()
	},
}

var kardianosStartCmd = &cobra.Command{
	Use:    "start",
	Short:  "[internal] kardianos svc.Start() under sudo",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		return svc.Start()
	},
}

var kardianosStopCmd = &cobra.Command{
	Use:    "stop",
	Short:  "[internal] kardianos svc.Stop() under sudo",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		return svc.Stop()
	},
}

var kardianosRestartCmd = &cobra.Command{
	Use:    "restart",
	Short:  "[internal] kardianos svc.Restart() under sudo",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		return svc.Restart()
	},
}

func init() {
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

// restartAndWait restarts the kardianos service and polls /health
// until the new process is responsive. Prints a one-line stderr
// notice. No-op when the service isn't installed or isn't running.
// Used by `devm upgrade` post-install and by the PersistentPreRun
// drift auto-heal.
func restartAndWait(reason string) error {
	svc, err := newKardianosService()
	if err != nil {
		return err
	}
	st, err := svc.Status()
	if err != nil || st != service.StatusRunning {
		return nil
	}
	fmt.Fprintf(os.Stderr, "restarting devm service (%s)…\n", reason)
	if err := runKardianosUnderSudo("restart"); err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	c := serviceapi.NewClient()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.Available(context.Background()) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("service did not become healthy within 5s after restart")
}
