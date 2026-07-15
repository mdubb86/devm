package main

import (
	"fmt"
	"os"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
)

var (
	statusJSON bool
	statusAll  bool
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status, plus sandbox state when run inside a project",
	Long: `Reports whether the devm daemon is running, where its binary lives,
and whether that binary matches this CLI (Fingerprint check). When
run inside a project (devm.yaml present), also reports sandbox VM
state, sessions, pending config changes, routing, DNS, CA, and
reverse-proxy readiness.

With --all, reports a table summarizing every project the daemon
knows about instead — one row per project with VM state, iron-proxy
health, and whether reconcile is required. Works from any directory;
ignores cwd/project.

Exits with code 3 when the daemon binary's Fingerprint differs from
this CLI's — an actionable signal that ` + "`devm install`" + ` will fix.
Exits with code 4 when a running project's iron-proxy is MISSING or
STALE — an actionable signal that ` + "`devm reconcile`" + ` will fix.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		if statusAll {
			c := serviceapi.NewClient()
			rows, err := c.StatusAll(cmd.Context())
			if err != nil {
				return fmt.Errorf("daemon unreachable: %w", err)
			}
			if statusJSON {
				fmt.Println(orchestrator.FormatStatusAllJSON(rows))
			} else {
				orchestrator.UseColor = os.Getenv("NO_COLOR") == "" && isTerminal(os.Stdout)
				fmt.Print(orchestrator.FormatStatusAllText(rows))
			}
			if anyProjectNeedsReconcile(rows) {
				os.Exit(ExitReconcileRequired)
			}
			return nil
		}

		repoRoot, err := os.Getwd()
		if err != nil {
			return err
		}

		var res orchestrator.StatusResult
		if cfg, cfgErr := config.Load(repoRoot); cfgErr == nil {
			// Project mode: full status including sandbox VM, routing,
			// DNS, CA, proxy — plus daemon status via ProbeDaemon.
			tr := tart.New()
			res, err = orchestrator.RunStatus(cfg, tr, repoRoot, Fingerprint)
			if err != nil {
				return err
			}
		} else {
			// No devm.yaml — daemon-only mode. Report just the daemon
			// probe so `devm status` outside a project still works.
			res = orchestrator.StatusResult{
				HasProject: false,
				Daemon:     orchestrator.ProbeDaemon(cmd.Context(), Fingerprint),
			}
		}

		if statusJSON {
			fmt.Println(orchestrator.FormatStatusJSON(res))
		} else {
			orchestrator.UseColor = os.Getenv("NO_COLOR") == "" && isTerminal(os.Stdout)
			fmt.Print(orchestrator.FormatStatusText(res))
		}

		// Drift is an exit-3 condition — actionable ("run devm install")
		// distinct from generic status failure. Daemon-touching commands
		// fail fast on drift via requireDaemonInSync; `devm status` is
		// the read-only probe and shouldn't fail — it renders the
		// mismatch in its output (red MISMATCH marker) and exits 3 so
		// scripts can key off the code.
		if res.Daemon.Fingerprint != "" && !res.Daemon.FingerprintMatchesCLI {
			os.Exit(ExitDaemonDrift)
		}

		// Reconcile-required is an exit-4 condition: the project's
		// iron-proxy is MISSING/STALE. Same "report, don't heal" rule as
		// drift above — `devm reconcile` is the sole heal path.
		if res.ProxyHealth != nil && res.ProxyHealth.Status != serviceapi.ProxyOK {
			os.Exit(ExitReconcileRequired)
		}
		return nil
	},
}

// anyProjectNeedsReconcile reports whether any row's iron-proxy is
// unhealthy on a running VM — the same condition FormatStatusAllText
// marks "required" in the RECONCILE column. Stopped VMs are excluded:
// their iron-proxy state isn't actionable until the VM comes back up.
func anyProjectNeedsReconcile(rows []serviceapi.ProjectStatus) bool {
	for _, r := range rows {
		if r.VMRunning && r.Proxy.Status != serviceapi.ProxyOK {
			return true
		}
	}
	return false
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Emit JSON output")
	statusCmd.Flags().BoolVar(&statusAll, "all", false, "Show a cross-project status table instead of the current project's status")
	rootCmd.AddCommand(statusCmd)
}
