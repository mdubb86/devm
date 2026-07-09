package main

import (
	"fmt"
	"os"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/orchestrator"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/spf13/cobra"
)

var (
	statusJSON bool
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status, plus sandbox state when run inside a project",
	Long: `Reports whether the devm daemon is running, where its binary lives,
and whether that binary matches this CLI (Fingerprint check). When
run inside a project (devm.yaml present), also reports sandbox VM
state, sessions, pending config changes, routing, DNS, CA, and
reverse-proxy readiness.

Exits with code 3 when the daemon binary's Fingerprint differs from
this CLI's — an actionable signal that ` + "`devm install`" + ` will fix.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
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
			fmt.Print(orchestrator.FormatStatusText(res))
		}

		// Drift is an exit-3 condition — actionable ("run devm install")
		// distinct from generic status failure. The daemon-up-with-drift
		// case is already caught by PersistentPreRun's ensureDaemonInSync
		// before we get here; this covers the daemon-down-but-on-disk-
		// binary-mismatches case that ensureDaemonInSync can't (it fails
		// open on unreachable daemon).
		if res.Daemon.Fingerprint != "" && !res.Daemon.FingerprintMatchesCLI {
			os.Exit(ExitDaemonDrift)
		}
		return nil
	},
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Emit JSON output")
	rootCmd.AddCommand(statusCmd)
}
