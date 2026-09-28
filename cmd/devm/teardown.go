package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
)

var teardownYes bool

var teardownCmd = &cobra.Command{
	Use:   "teardown",
	Short: "Destroy the VM entirely (deletes disk)",
	Long: `Stops the project VM and deletes its disk image. All installed
state is lost. The workspace volume is preserved; a fresh devm start
will re-run install/startup.

Prompts on a terminal; refuses non-interactively unless --yes (-y)
is passed.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ident := cfg // capture package identity cfg before it's shadowed below
		resolved, err := discoverProjectFn()
		if err != nil {
			return err
		}
		cfg, err := config.Load(resolved.MacCwd)
		if err != nil {
			return err
		}
		if err := daemonHandshake(cmd.Context(), ident, cfg); err != nil {
			return err
		}

		ok, err := confirmDestructive("Tear down VM "+cfg.Project.Name+"?", teardownYes)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "aborted")
			os.Exit(1)
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		// Remove this project's routes from the daemon. Best-effort:
		// silent if the daemon is down. The "I'm done with this
		// project" signal per the Ship 3 design.
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
		c := serviceapi.NewClient(ident)
		if c.Available(rctx) {
			_ = c.RemoveRoutes(rctx, cfg.Project.Name)
		}
		rcancel()

		deps := orchestrator.StopDeps{
			Tart:             tart.New(),
			ServiceAPIClient: c,
			Ident:            ident,
		}
		rc, err := orchestrator.RunStop(ctx, deps, cfg.Project.Name, orchestrator.StopDestroy)
		if err != nil {
			return err
		}
		if rc != 0 {
			os.Exit(rc)
		}
		return nil
	},
}

func init() {
	teardownCmd.Flags().BoolVarP(&teardownYes, "yes", "y", false, "Run without the interactive confirmation prompt")
	rootCmd.AddCommand(teardownCmd)
}
