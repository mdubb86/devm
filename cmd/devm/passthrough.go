package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/repohelpers"
	"github.com/mdubb86/devm/internal/serviceapi"
)

var passthroughCmd = &cobra.Command{
	Use:   "passthrough",
	Short: "Temporarily open egress for a supervised window (default 30s)",
	Long: `Flips this project's authority mode to passthrough for a bounded window
so you can supervise a command that needs broader access than the
project's allowlist covers (a one-off ` + "`curl … | bash`" + `, a plugin
fetch, an ad-hoc apt-get from an unusual mirror). The window auto-restores
after ` + "`--for`" + ` (default 30s); ` + "`devm restrict`" + ` closes it early.

During the passthrough window, authority is deferred — iron-proxy remains
in the traffic path, MITM'ing + audit-logging + secret-substituting as usual,
but gates based on authority (you) rather than the project's allowlist. Meant
for commands you supervise in real time. The timer is a safety net, not
a substitute for supervision: anything exfiltrated during the window
stays exfiltrated after it closes.`,
	RunE: func(cmd *cobra.Command, args []string) error {
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

		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()

		forDur, err := cmd.Flags().GetDuration("for")
		if err != nil {
			return err
		}
		durationSeconds := 0
		if cmd.Flags().Changed("for") {
			if forDur < time.Second {
				return fmt.Errorf("--for must be at least 1s (got %s); omit the flag entirely for the 30s default", forDur)
			}
			durationSeconds = int(forDur.Round(time.Second) / time.Second)
		}

		wasOpen, expiresSeconds, err := serviceapi.NewClient(ident).PassthroughEgress(ctx, cfg.Project.Name, durationSeconds)
		if err != nil {
			return fmt.Errorf("passthrough egress: %w", err)
		}
		verb := "opened"
		if wasOpen {
			verb = "renewed"
		}
		fmt.Printf("egress PASSTHROUGH — %s for %s (auto-restores; run `devm restrict` to close early)\n",
			verb, (time.Duration(expiresSeconds) * time.Second).String())
		return nil
	},
}

var restrictCmd = &cobra.Command{
	Use:   "restrict",
	Short: "Close an active `devm passthrough` window immediately",
	Long: `Restores this project's egress policy to RESTRICTED, ending a
` + "`devm passthrough`" + ` window before its timer fires. No-op if no
window is active.`,
	RunE: func(cmd *cobra.Command, args []string) error {
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

		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()

		wasOpen, err := serviceapi.NewClient(ident).RestrictEgress(ctx, cfg.Project.Name)
		if err != nil {
			return fmt.Errorf("restrict egress: %w", err)
		}
		if !wasOpen {
			fmt.Println("egress was already RESTRICTED (no active passthrough window)")
			return nil
		}
		fmt.Println("egress RESTRICTED")
		return nil
	},
}

func init() {
	passthroughCmd.Flags().Duration("for", 0,
		"How long to keep egress open before it auto-restores (0 = daemon default, 30s)")
	rootCmd.AddCommand(passthroughCmd, restrictCmd)
}
