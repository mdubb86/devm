package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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
	prog := &kardianosProgram{}
	cfg := &service.Config{
		Name:        "com.devm.service",
		DisplayName: "devm",
		Description: "devm Mac-side service: hostname routing, egress proxy, sandbox orchestration",
		Executable:  exe,
		Arguments:   []string{"serve"},
		Option: service.KeyValue{
			"UserService":   true, // LaunchAgent, not LaunchDaemon
			"LaunchdConfig": serviceapi.LaunchdPlistTemplate,
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

		// DNS resolver setup — sudo only when actually needed.
		dnsOK := setupDNS()

		// CA trust — sudo only when needed.
		if dnsOK {
			setupCATrust()
		}

		return nil
	},
}

// setupDNS configures the /etc/resolver/test file if needed.
// Returns true if DNS setup succeeded or was already in place,
// false if a fatal check error occurred (further setup is skipped).
func setupDNS() bool {
	state, err := serviceapi.CheckResolverFile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: could not check %s: %v\n",
			serviceapi.ResolverFilePath, err)
		fmt.Println("devm service installed and started.")
		return false
	}
	switch state {
	case serviceapi.ResolverFileMatches:
		fmt.Println("devm service installed; DNS resolver already configured.")
	case serviceapi.ResolverFileMissing:
		fmt.Println("devm service installed.")
		fmt.Println("Setting up DNS resolver for .test (requires sudo)...")
		if err := serviceapi.WriteResolverFile(); err != nil {
			fmt.Fprintf(os.Stderr,
				"note: DNS not configured (%v). Re-run `devm install` to retry.\n",
				err,
			)
			return true // DNS partial failure is non-fatal; proceed to CA
		}
		fmt.Println("DNS resolver configured.")
	case serviceapi.ResolverFileDiverged:
		fmt.Println("devm service installed.")
		fmt.Printf("note: %s exists but doesn't match — overwriting (requires sudo).\n",
			serviceapi.ResolverFilePath)
		if err := serviceapi.WriteResolverFile(); err != nil {
			fmt.Fprintf(os.Stderr,
				"note: DNS not configured (%v). Re-run `devm install` to retry.\n",
				err,
			)
			return true // DNS partial failure is non-fatal; proceed to CA
		}
		fmt.Println("DNS resolver configured.")
	}
	return true
}

// setupCATrust installs the devm CA root into the System Keychain if not
// already present. Sudo is only prompted when the cert is absent.
func setupCATrust() {
	trusted, err := serviceapi.CheckCATrusted()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"note: could not check CA trust state: %v\n", err)
		return
	}
	if trusted {
		fmt.Println("CA already trusted.")
		return
	}

	// Ensure the CA root is generated. Normally the daemon does this at
	// startup; we trigger it here if it hasn't yet so install can
	// immediately offer to trust it.
	_, err = serviceapi.LoadOrGenerate()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"note: could not generate CA: %v. Re-run `devm install`.\n",
			err,
		)
		return
	}

	runDir, err := serviceapi.EnsureRuntimeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"note: could not resolve CA cert path: %v\n", err)
		return
	}
	rootCertPath := filepath.Join(runDir, "ca", "root.crt")

	fmt.Println("Trusting devm Local CA (requires sudo)...")
	if err := serviceapi.InstallCATrust(rootCertPath); err != nil {
		fmt.Fprintf(os.Stderr,
			"note: CA not trusted (%v). HTTPS will show browser warnings until you re-run `devm install`.\n",
			err,
		)
		return
	}
	fmt.Println("CA trusted.")
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

		// DNS resolver teardown — sudo only when our file is present.
		state, _ := serviceapi.CheckResolverFile()
		switch state {
		case serviceapi.ResolverFileMatches:
			fmt.Println("Removing DNS resolver (requires sudo)...")
			if err := serviceapi.RemoveResolverFile(); err != nil {
				fmt.Fprintf(os.Stderr,
					"note: %s remains (%v).\n",
					serviceapi.ResolverFilePath, err,
				)
			} else {
				fmt.Println("DNS resolver removed.")
			}
		case serviceapi.ResolverFileDiverged:
			fmt.Fprintf(os.Stderr,
				"note: %s exists but doesn't match devm's config — leaving it.\n",
				serviceapi.ResolverFilePath,
			)
		case serviceapi.ResolverFileMissing:
			// Nothing to do.
		}

		// CA trust removal — only if currently trusted.
		trusted, _ := serviceapi.CheckCATrusted()
		if trusted {
			fmt.Println("Removing devm CA from System Keychain (requires sudo)...")
			if err := serviceapi.UninstallCATrust(); err != nil {
				fmt.Fprintf(os.Stderr,
					"note: CA trust remains (%v).\n", err)
			} else {
				fmt.Println("CA trust removed.")
			}
		}

		return nil
	},
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
