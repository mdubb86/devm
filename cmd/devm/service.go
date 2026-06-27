package main

import (
	"context"
	"fmt"
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
		p.done <- serviceapi.RunService(ctx, Version)
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
		// Hard-break: refuse if the Ship 4-era LaunchAgent plist is still around.
		_, home, err := resolveInstallUser(nil)
		if err != nil {
			return err
		}
		oldPlist := filepath.Join(home, "Library", "LaunchAgents", "com.devm.service.plist")
		if err := checkOldLaunchAgentPlist(oldPlist); err != nil {
			return err
		}
		// All install work — plist write, launchctl bootstrap,
		// resolver, CA — happens inside runPrivilegedInstall's
		// single sudo block. No user-side svc.Install / svc.Start.
		runPrivilegedInstall()

		// Tart base-image build (user context, unchanged).
		imageDir, err := image.ImageDirFromExe()
		if err != nil {
			fmt.Fprintf(os.Stderr, "note: could not locate image directory: %v\n", err)
			return nil
		}
		needs, _, err := image.NeedsBuild(imageDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "note: image hash check failed: %v\n", err)
			return nil
		}
		if needs {
			fmt.Println("Building devm-base Tart image (1-2 min)...")
			if err := image.BuildBaseImage(cmd.Context(), imageDir, os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "note: base image build failed (%v). Re-run `devm install` to retry.\n", err)
				return nil
			}
			fmt.Println("devm-base built.")
		} else {
			fmt.Println("devm-base is up to date.")
		}
		return nil
	},
}

func runPrivilegedInstall() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: could not locate executable: %v\n", err)
		return
	}

	dnsState, err := serviceapi.CheckResolverFile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: could not check %s: %v\n",
			serviceapi.ResolverFilePath, err)
		return
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
			fmt.Fprintf(os.Stderr,
				"note: could not generate CA: %v. Re-run `devm install`.\n", err)
			return
		}
		runDir, err := serviceapi.EnsureRuntimeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "note: could not resolve CA cert path: %v\n", err)
			return
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

	fmt.Println("Running privileged install (1 sudo prompt expected)...")
	c := exec.Command("sudo", "bash", "-c", sb.String())
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		fmt.Fprintf(os.Stderr,
			"note: privileged install failed (%v). Re-run `devm install`.\n", err)
	}
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
		// All uninstall work happens inside runPrivilegedUninstall's
		// single sudo block.
		runPrivilegedUninstall()
		// Clean up any leftover socket.
		_ = os.Remove(serviceapi.SocketPath())
		fmt.Println("devm uninstalled. Runtime dir (~/Library/Application Support/devm/) preserved; rm -rf to wipe.")
		return nil
	},
}

func runPrivilegedUninstall() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: could not locate executable: %v\n", err)
		return
	}

	// Build the single sudo script. set +e: best-effort each step
	// so a partial state still cleans up as much as possible.
	var sb strings.Builder
	sb.WriteString("set +e\n")
	fmt.Fprintf(&sb, "%s _kardianos uninstall\n", shellQuote(exe))
	fmt.Fprintf(&sb, "rm -f %s\n", shellQuote(serviceapi.ResolverFilePath))
	fmt.Fprintf(&sb, "security delete-certificate -c %s %s 2>/dev/null\n",
		shellQuote(serviceapi.CATrustCertCN), shellQuote(serviceapi.SystemKeychain))

	fmt.Println("Running privileged uninstall (1 sudo prompt expected)...")
	c := exec.Command("sudo", "bash", "-c", sb.String())
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		fmt.Fprintf(os.Stderr,
			"note: privileged uninstall had issues (%v). Some state may remain.\n", err)
	}
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
		return svc.Install()
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

// checkOldLaunchAgentPlist refuses to proceed if the Ship 4-era
// user-level LaunchAgent plist still exists. The new system-level
// LaunchDaemon and the old LaunchAgent can't both manage
// com.devm.service. We don't auto-migrate (single-user repo, hard
// breaks preferred) — the user removes it manually with the printed
// command and re-runs install.
func checkOldLaunchAgentPlist(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("a previous-version devm install exists at %s\n\nRemove it first:\n\n  launchctl bootout gui/$UID/com.devm.service 2>/dev/null\n  rm %s\n\nThen re-run `devm install`.", path, path)
	}
	return nil
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
