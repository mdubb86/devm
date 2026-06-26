package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kardianos/service"
	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/serviceapi"
)

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
	// Substitute log paths into the plist template before handing to
	// kardianos. launchd's StandardOutPath/ErrorPath need absolute
	// paths — no $HOME expansion — and kardianos's template engine
	// doesn't expose the user's home dir.
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home dir: %w", err)
	}
	logDir := filepath.Join(home, "Library", "Logs")
	plistText := strings.NewReplacer(
		"__LOG_OUT__", filepath.Join(logDir, "com.devm.service.out.log"),
		"__LOG_ERR__", filepath.Join(logDir, "com.devm.service.err.log"),
	).Replace(serviceapi.LaunchdPlistTemplate)

	prog := &kardianosProgram{}
	cfg := &service.Config{
		Name:        "com.devm.service",
		DisplayName: "devm",
		Description: "devm Mac-side service: hostname routing, egress proxy, sandbox orchestration",
		Executable:  exe,
		Arguments:   []string{"serve"},
		Option: service.KeyValue{
			"UserService":   true, // LaunchAgent, not LaunchDaemon
			"LaunchdConfig": plistText,
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
		// Run() blocks until the service stops. On non-service
		// invocation (e.g., `devm serve` from a shell), it runs
		// in foreground mode and respects ctrl-c.
		return svc.Run()
	},
}

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Register devm as a user-level launchd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		if err := svc.Install(); err != nil {
			return fmt.Errorf("install: %w", err)
		}
		if err := svc.Start(); err != nil {
			return fmt.Errorf("start after install: %w", err)
		}

		// All privileged setup (DNS resolver file + CA trust) runs
		// under a single sudo invocation so the user sees exactly one
		// password prompt when anything's actually needed.
		runPrivilegedInstall()
		return nil
	},
}

// runPrivilegedInstall consolidates DNS resolver setup and CA trust
// install into one sudo call. Both checks (CheckResolverFile,
// CheckCATrusted) are unprivileged; we only shell out to sudo when
// at least one step actually needs to happen. Re-running install
// after everything is in place produces zero sudo prompts.
func runPrivilegedInstall() {
	dnsState, err := serviceapi.CheckResolverFile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: could not check %s: %v\n",
			serviceapi.ResolverFilePath, err)
		fmt.Println("devm service installed and started.")
		return
	}
	needsDNS := dnsState != serviceapi.ResolverFileMatches

	trusted, err := serviceapi.CheckCATrusted()
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: could not check CA trust state: %v\n", err)
		// Treat as trusted to avoid spurious sudo prompts when the
		// check itself broke (e.g., `security` not on PATH).
		trusted = true
	}
	needsCA := !trusted

	if !needsDNS && !needsCA {
		fmt.Println("devm service installed; DNS resolver and CA trust already configured.")
		return
	}

	// CA generation is unprivileged (writes to ~/Library/Application
	// Support/devm/ca/); do it before the sudo block so the script
	// has a cert file to point at.
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

	// Compose the shell script. set -e: bail if any step fails so
	// partial state is obvious rather than silent.
	var sb strings.Builder
	sb.WriteString("set -e\n")
	if needsDNS {
		if dnsState == serviceapi.ResolverFileDiverged {
			fmt.Printf("note: %s exists but doesn't match — overwriting.\n",
				serviceapi.ResolverFilePath)
		}
		sb.WriteString("mkdir -p /etc/resolver\n")
		// Single-quoted heredoc terminator so the content goes in
		// verbatim with no shell interpolation.
		fmt.Fprintf(&sb, "cat > %s <<'EOF'\n%sEOF\n",
			serviceapi.ResolverFilePath, serviceapi.CanonicalResolverContents())
	}
	if needsCA {
		fmt.Fprintf(&sb, "security add-trusted-cert -d -r trustRoot -k %s %s\n",
			shellQuote(serviceapi.SystemKeychain), shellQuote(rootCertPath))
	}

	var todo []string
	if needsDNS {
		todo = append(todo, "DNS resolver")
	}
	if needsCA {
		todo = append(todo, "CA trust")
	}
	fmt.Printf("devm service installed. Setting up %s (requires sudo)...\n",
		strings.Join(todo, " + "))

	scriptCmd := exec.Command("sudo", "sh", "-c", sb.String())
	scriptCmd.Stdin = os.Stdin
	scriptCmd.Stdout = os.Stdout
	scriptCmd.Stderr = os.Stderr
	if err := scriptCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr,
			"note: privileged setup failed (%v). Re-run `devm install`.\n", err)
		return
	}

	if needsDNS {
		fmt.Println("DNS resolver configured.")
	}
	if needsCA {
		fmt.Println("CA trusted.")
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
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		// Best-effort stop before uninstall.
		_ = svc.Stop()
		if err := svc.Uninstall(); err != nil {
			return fmt.Errorf("uninstall: %w", err)
		}
		// Clean up any leftover socket.
		_ = os.Remove(serviceapi.SocketPath())
		fmt.Println("devm service uninstalled.")

		// All privileged teardown (DNS resolver file + CA trust)
		// runs under a single sudo invocation. Symmetric with install.
		runPrivilegedUninstall()
		return nil
	},
}

// runPrivilegedUninstall consolidates DNS resolver removal and CA
// trust removal into one sudo call. Both checks are unprivileged;
// we only shell out to sudo when at least one removal is actually
// needed. A divergent resolver file is left alone (with a warning)
// — it's not ours to touch.
func runPrivilegedUninstall() {
	dnsState, _ := serviceapi.CheckResolverFile()
	dropsDNS := dnsState == serviceapi.ResolverFileMatches
	if dnsState == serviceapi.ResolverFileDiverged {
		fmt.Fprintf(os.Stderr,
			"note: %s exists but doesn't match devm's config — leaving it.\n",
			serviceapi.ResolverFilePath)
	}

	trusted, _ := serviceapi.CheckCATrusted()
	dropsCA := trusted

	if !dropsDNS && !dropsCA {
		return
	}

	// Compose the shell script. set +e: don't fail the entire
	// teardown if one piece is already gone or otherwise hiccups.
	// We want best-effort removal of whatever's still there.
	var sb strings.Builder
	sb.WriteString("set +e\n")
	if dropsDNS {
		fmt.Fprintf(&sb, "rm -f %s\n", shellQuote(serviceapi.ResolverFilePath))
	}
	if dropsCA {
		fmt.Fprintf(&sb, "security delete-certificate -c %s -t %s\n",
			shellQuote(serviceapi.CATrustCertCN),
			shellQuote(serviceapi.SystemKeychain))
	}

	var todo []string
	if dropsDNS {
		todo = append(todo, "DNS resolver")
	}
	if dropsCA {
		todo = append(todo, "CA trust")
	}
	fmt.Printf("Removing %s (requires sudo)...\n", strings.Join(todo, " + "))

	scriptCmd := exec.Command("sudo", "sh", "-c", sb.String())
	scriptCmd.Stdin = os.Stdin
	scriptCmd.Stdout = os.Stdout
	scriptCmd.Stderr = os.Stderr
	if err := scriptCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr,
			"note: privileged teardown had issues (%v). Some state may remain.\n", err)
		return
	}

	if dropsDNS {
		fmt.Println("DNS resolver removed.")
	}
	if dropsCA {
		fmt.Println("CA trust removed.")
	}
}

var serviceStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the devm service",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		if err := svc.Start(); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		fmt.Println("devm service started.")
		return nil
	},
}

var serviceStopCmd = &cobra.Command{
	Use:   "stop-service",
	Short: "Stop the devm service",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		if err := svc.Stop(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		fmt.Println("devm service stopped.")
		return nil
	},
}

var serviceRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the devm service",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		if err := svc.Restart(); err != nil {
			return fmt.Errorf("restart: %w", err)
		}
		fmt.Println("devm service restarted.")
		return nil
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "service-status",
	Short: "Show whether the devm service is running",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		svc, err := newKardianosService()
		if err != nil {
			return err
		}
		st, err := svc.Status()
		if err != nil {
			return fmt.Errorf("status: %w", err)
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

func init() {
	rootCmd.AddCommand(serveCmd, installCmd, uninstallCmd,
		serviceStartCmd, serviceStopCmd, serviceRestartCmd, serviceStatusCmd)
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
	if err := svc.Restart(); err != nil {
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
