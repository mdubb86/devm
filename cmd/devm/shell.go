package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
)

var shellCmd = &cobra.Command{
	Use:   "shell [-- COMMAND...]",
	Short: "Bootstrap the sandbox (if needed) and attach an interactive session",
	Long: `Acquires a project-local lock, brings the sandbox to a running state
if it is stopped, reconciles ports, then attaches an interactive shell.
The sandbox stays running after the shell exits — use ` + "`devm stop`" + ` to
stop it or ` + "`devm teardown`" + ` to destroy it.

If the sandbox is already running, devm shell skips bootstrap and
attaches immediately. Port reconcile only runs on cold start.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmdName := "bash"
		var cmdArgs []string
		if len(args) > 0 {
			cmdName = args[0]
			cmdArgs = args[1:]
		}
		return runShellFlow(cmd, cmdName, cmdArgs)
	},
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Bring the sandbox up without attaching a shell",
	Long: `Cold-starts the sandbox (or attaches to an already-running one) and
returns immediately. Equivalent to ` + "`devm shell -- true`" + ` but with
clearer intent — useful in scripts, CI, or when you want the VM
warmed up in the background before you attach later.

The sandbox stays running until ` + "`devm stop`" + ` or ` + "`devm teardown`" + `.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("devm start takes no arguments (got %v)", args)
		}
		// Run `true` inside the VM so the flow completes provisioning and
		// service start without opening an interactive shell. `true` is
		// portable, exits 0, and takes no time.
		return runShellFlow(cmd, "true", nil)
	},
}

var execCmd = &cobra.Command{
	Use:   "exec [--] COMMAND [ARGS...]",
	Short: "Run a one-shot command inside a running sandbox",
	Long: `Runs COMMAND inside the sandbox with the project env sourced and cwd
set to the workspace directory. Returns COMMAND's exit code directly —
designed for scripts and CI.

Requires the sandbox to already be running: exec fails loud if the VM
is stopped or absent. This matches the ` + "`docker exec`" + ` / ` + "`kubectl exec`" + `
convention — bring the sandbox up with ` + "`devm start`" + ` (or ` + "`devm shell`" + `)
first, then exec into it.

TTY/PTY handling is auto-detected from the caller's stdin:
  - stdin is a terminal → PTY allocated (so ` + "`devm exec bash`" + ` acts
    like an interactive shell).
  - stdin is piped/redirected → plain pipes, exit code forwarded.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ident := cfg // capture package identity cfg before it's shadowed below
		repoRoot, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get cwd: %w", err)
		}
		cfg, err := config.Load(repoRoot)
		if err != nil {
			return err
		}
		// daemonHandshake (fingerprint drift check + iron-proxy warning) is
		// NOT called here — runShellFlow does it below, before any real
		// work. Calling it here too would run the check twice and print
		// any iron-proxy-drift warning twice.
		if err := requireRunningVM(cmd.Context(), ident, cfg); err != nil {
			return err
		}
		return runShellFlow(cmd, args[0], args[1:])
	},
	// Don't try to parse flags in the exec'd command's argv — e.g.
	// `devm exec ls -la` must pass -la to ls, not to devm.
	DisableFlagParsing: true,
}

// requireRunningVM returns a clear error when the project's sandbox
// isn't running — used by `devm exec` to enforce the docker-exec
// convention (fail loud on stopped/absent, don't silently cold-start).
//
// ident is the daemon identity (prod vs. e2e); named "ident" rather
// than "cfg" here because cfg is the caller's project schema.Config.
func requireRunningVM(ctx context.Context, ident identity.Config, cfg schema.Config) error {
	c := serviceapi.NewClient(ident)
	st, err := c.VMStatus(ctx, cfg.Project.Name)
	if err != nil {
		return fmt.Errorf("query vm status: %w", err)
	}
	if !st.Running {
		return fmt.Errorf("sandbox %q is not running — start it with `devm start` (or `devm shell`) first", cfg.Project.Name)
	}
	return nil
}

// runShellFlow is the shared cold-start / attach implementation used by
// both `devm shell` and `devm start`. cmdName + cmdArgs is what runs
// inside the VM after bootstrap; "true" from `devm start` returns
// immediately, "bash" from `devm shell` attaches an interactive session.
func runShellFlow(cmd *cobra.Command, cmdName string, cmdArgs []string) error {
	// Past arg parsing — errors from here on are runtime, not usage.
	cmd.SilenceUsage = true
	ident := cfg // capture package identity cfg before it's shadowed below
	repoRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}
	cfg, err := config.Load(repoRoot)
	if err != nil {
		return err
	}
	if err := daemonHandshake(cmd.Context(), ident, cfg); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Auto-install routes in vm mode if the project doesn't have
	// any yet. Best-effort: silent if the daemon is down. We
	// don't overwrite an existing route set — the user may have
	// explicitly chosen `devm route local`, and we respect that
	// across stop/start cycles per the Ship 3 design.
	//
	// This goroutine is launched BEFORE orchestrator.RunShell brings the
	// VM up below, so on a cold start buildRoutes(ModeVM) — which reads
	// the VM's IP — fails until the VM exists. Retry on error (VM not up
	// yet) until it succeeds or a generous cold-start-sized deadline
	// passes, so routes still get registered during provisioning,
	// before an interactive attach or a `-- cmd` exit. Retry ONLY on
	// error; once buildRoutes returns nil error, stop — an empty result
	// is legitimate (no hostnamed services) and not a reason to keep
	// retrying.
	go func() {
		var routes []serviceapi.Route
		deadline := time.Now().Add(5 * time.Minute)
		for {
			r, err := buildRoutes(cfg, serviceapi.ModeVM)
			if err == nil {
				routes = r
				break
			}
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(2 * time.Second)
		}
		if len(routes) == 0 {
			return
		}
		rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer rcancel()
		c := serviceapi.NewClient(ident)
		if !c.Available(rctx) {
			return
		}
		existing, err := c.ListRoutes(rctx)
		if err != nil {
			return
		}
		if _, present := existing[cfg.Project.Name]; present {
			return
		}
		_ = c.ApplyRoutes(rctx, cfg.Project.Name, routes)
	}()

	deps := orchestrator.DefaultShellDeps(ident, repoRoot)
	rc, err := orchestrator.RunShell(ctx, deps, cfg, repoRoot, cfg.Project.Name, cmdName, cmdArgs)
	if err != nil {
		// SIGINT during cold start cancels ctx. Suppress the noisy
		// "context canceled" stack and exit 130 (SIGINT convention).
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "aborted")
			os.Exit(130)
		}
		return err
	}
	if rc != 0 {
		os.Exit(rc)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(shellCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(execCmd)
}
