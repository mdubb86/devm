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
	"github.com/mdubb86/devm/internal/repohelpers"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
)

var shellCmd = &cobra.Command{
	Use:   "shell [-- COMMAND...]",
	Short: "Attach a shell to a running sandbox (warm attach only)",
	Long: `Attaches an interactive shell to the running, provisioned sandbox
for this project. Does NOT start or provision a stopped sandbox — for
that, run ` + "`devm start`" + ` first, then ` + "`devm shell`" + `.

If the sandbox is stopped or has not been provisioned yet, prints a
clear error and exits non-zero. This is the intentional split: the
approve gate refuses at the single point that reads devm.yaml, and
` + "`devm shell`" + ` should never accidentally cold-start.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cmdName := "bash"
		var cmdArgs []string
		if len(args) > 0 {
			cmdName = args[0]
			cmdArgs = args[1:]
		}

		ident := cfg // capture package identity cfg before it's shadowed below
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get cwd: %w", err)
		}
		repoRoot, err := repohelpers.FindDevmYAML(cwd)
		if err != nil {
			return err
		}
		projectName, err := config.ReadProjectName(repoRoot)
		if err != nil {
			return err
		}
		pcfg := schema.Config{Project: schema.Project{Name: projectName}}
		// daemonHandshake (fingerprint drift check + iron-proxy warning) is
		// called explicitly here since RunAttach, unlike runShellFlow,
		// never cold-starts and so never calls it on its own.
		if err := daemonHandshake(cmd.Context(), ident, pcfg); err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		deps := orchestrator.DefaultShellDeps(ident, repoRoot)
		rc, err := orchestrator.RunAttach(ctx, deps, pcfg.Project.Name, repoRoot, cmdName, cmdArgs, os.Stderr)
		if err != nil {
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
	},
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Cold-start (or adopt-in-place) the sandbox",
	Long: `Brings the sandbox up: cold-starts if stopped, adopts in place if
running-but-not-provisioned. This is the sole command that reads
` + "`devm.yaml`" + ` for apply, so it is also the sole command that
refuses when devm.yaml has changed since it was last approved.

Returns after the VM is provisioned and ` + "`devm.target`" + ` is up. Attach a
shell with ` + "`devm shell`" + `.`,
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

// stripLeadingDashDash removes a leading "--" from args. With
// DisableFlagParsing: true, cobra passes the standard end-of-flags
// marker through as a literal arg; strip it so the guest command
// (args[0] after strip) isn't "--".
func stripLeadingDashDash(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

// handleExecHelpFlag prints usage and returns true when args is
// exactly {"--help"} or {"-h"} — the shape a user would type to
// request help. Any other shape (e.g. `--help extra` or `-h extra`)
// is passed through: the user might genuinely be running a guest
// command called "--help" or "-h", or passing --help/-h to a guest
// tool.
func handleExecHelpFlag(args []string, cmd *cobra.Command) bool {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_ = cmd.Help()
		return true
	}
	return false
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
		if handleExecHelpFlag(args, cmd) {
			return nil
		}
		args = stripLeadingDashDash(args)
		if len(args) == 0 {
			return fmt.Errorf("exec requires a COMMAND — see `devm exec --help`")
		}
		ident := cfg // capture package identity cfg before it's shadowed below
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get cwd: %w", err)
		}
		repoRoot, err := repohelpers.FindDevmYAML(cwd)
		if err != nil {
			return err
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
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}
	repoRoot, err := repohelpers.FindDevmYAML(cwd)
	if err != nil {
		return err
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
	// any yet. Best-effort: silent if the daemon is down. We don't
	// overwrite an existing route set — the user may have explicitly
	// chosen `devm route local`, and we respect that across stop/start
	// cycles per the Ship 3 design.
	//
	// Races with orchestrator.RunShell below, which is what brings the
	// VM up. Routes/apply on the daemon side needs the project's IP
	// allocated (ironProxyState.get) — until /vm/start has completed
	// that step, /routes/apply returns 400 "no projectIP allocated".
	// So the loop retries on ApplyRoutes failure, not buildRoutes
	// (which never errors — the VM-IP substitution happens server-side).
	// Deadline is cold-start-sized; if the goroutine gives up, the
	// user's fix is a manual `devm reconcile --yes`.
	go func() {
		routes, err := buildRoutes(cfg, serviceapi.ModeVM)
		if err != nil || len(routes) == 0 {
			return
		}
		c := serviceapi.NewClient(ident)
		deadline := time.Now().Add(5 * time.Minute)
		for {
			rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
			if c.Available(rctx) {
				// Preserve an existing user-chosen route mode (e.g.
				// `devm route local`) across stop/start; only fill in
				// when the daemon has nothing for this project.
				existing, listErr := c.ListRoutes(rctx)
				if listErr == nil {
					if _, present := existing[cfg.Project.Name]; present {
						rcancel()
						return
					}
					if _, applyErr := c.ApplyRoutes(rctx, cfg.Project.Name, routes); applyErr == nil {
						rcancel()
						return
					}
					// applyErr is typically the "no projectIP allocated"
					// 400 during the race window — keep polling until
					// /vm/start has populated ironProxyState.
				}
			}
			rcancel()
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(2 * time.Second)
		}
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
