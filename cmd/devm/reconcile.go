package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
)

var (
	reconcileYes  bool
	reconcileJSON bool
)

var reconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Diff devm.yaml against the running sandbox's daemon-side state; apply live changes or prompt for recreate",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		if err := requireDaemonInSync(cmd.Context()); err != nil {
			return err
		}
		repoRoot, err := os.Getwd()
		if err != nil {
			return err
		}
		cfg, err := config.Load(repoRoot)
		if err != nil {
			return err
		}
		tr := tart.New()
		rc, res, err := orchestrator.RunReconcile(cfg, tr, repoRoot, orchestrator.ReconcileOptions{})
		if err != nil {
			return err
		}
		if reconcileJSON {
			fmt.Println(orchestrator.FormatReconcileJSON(res))
		} else {
			fmt.Print(orchestrator.FormatReconcileText(res))
		}
		if rc != 0 {
			os.Exit(rc)
		}

		// Best-effort: re-push routes so a `direct` flip made by this
		// reconcile is reflected in the daemon's DNS answer right away,
		// instead of waiting for the next `devm shell` auto-apply.
		// buildRoutes(ModeVM) resolves the VM IP via tart and errors if
		// the VM isn't running — that's a normal skip (nothing live to
		// update on a stopped VM), not a failure. A down daemon or a
		// push error must never fail `devm reconcile` itself.
		if routes, err := buildRoutes(cfg, serviceapi.ModeVM); err == nil {
			func() {
				rctx, rcancel := context.WithTimeout(cmd.Context(), 3*time.Second)
				defer rcancel()
				c := serviceapi.NewClient()
				if !c.Available(rctx) {
					return
				}
				if err := c.ApplyRoutes(rctx, cfg.Project.Name, routes); err != nil {
					fmt.Fprintf(os.Stderr, "warning: re-push routes after reconcile: %v\n", err)
				}
			}()
		}

		if len(res.RecreateRequired) == 0 {
			return nil
		}

		// Recreate-required path: decide whether to prompt, and on
		// approval delegate to the same teardown + start helpers
		// `devm teardown` / `devm shell` already use directly.
		if !reconcileYes {
			if !isatty.IsTerminal(os.Stdin.Fd()) {
				os.Exit(2)
			}
			fmt.Print("[y/N]: ")
			var resp string
			_, _ = fmt.Fscanln(os.Stdin, &resp)
			if resp != "y" && resp != "Y" {
				os.Exit(1)
			}
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		stopDeps := orchestrator.StopDeps{
			Tart:             tr,
			ServiceAPIClient: serviceapi.NewClient(),
		}
		mode := orchestrator.StopPreserve
		if res.Flavor == reconcile.FlavorTeardownShell {
			mode = orchestrator.StopDestroy
		}
		if _, err := orchestrator.RunStop(ctx, stopDeps, cfg.Project.Name, mode, true); err != nil {
			return fmt.Errorf("recreate (%s): %w", res.Flavor, err)
		}

		return runShellFlow(cmd, "true", nil)
	},
}

func init() {
	reconcileCmd.Flags().BoolVarP(&reconcileYes, "yes", "y", false, "Skip the recreate confirmation prompt")
	reconcileCmd.Flags().BoolVar(&reconcileJSON, "json", false, "Emit JSON output")
	rootCmd.AddCommand(reconcileCmd)
}
