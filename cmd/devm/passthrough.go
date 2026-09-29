package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/serviceapi"
)

// parsePassthroughDuration accepts a Go duration string ("30s", "5m",
// "24h") and returns the whole-second count for the daemon. Empty or
// sub-second durations are rejected — there is no default and no
// implicit floor beyond 1s.
func parsePassthroughDuration(arg string) (int, error) {
	if arg == "" {
		return 0, fmt.Errorf("duration is required (e.g. 30s, 5m, 24h) — pass it as the positional argument")
	}
	d, err := time.ParseDuration(arg)
	if err != nil {
		return 0, fmt.Errorf("duration: %w (e.g. 30s, 5m, 24h)", err)
	}
	if d < time.Second {
		return 0, fmt.Errorf("duration must be at least 1s (got %s)", d)
	}
	return int(d.Round(time.Second) / time.Second), nil
}

var passthroughCmd = &cobra.Command{
	Use:   "passthrough",
	Short: "Manage this project's egress passthrough window",
	Long: `Manage the project's egress passthrough window.

Subcommands:
  open <duration>   Open a passthrough window immediately.
                    Duration is a positional Go duration (e.g. 30s, 5m, 24h)
                    and is required — there is no default.
  close             Close an active window immediately.
  approve           Open a window honoring a pending gdevm passthrough request.
  deny              Clear a pending gdevm passthrough request without opening.

During a passthrough window, iron-proxy stays in the path (MITM + audit +
secret substitution) but the per-request allowlist check is bypassed for
the window's duration. The timer is a safety net, not a substitute for
supervision: anything exfiltrated during the window stays exfiltrated
after it closes.`,
}

var passthroughOpenCmd = &cobra.Command{
	Use:   "open <duration>",
	Short: "Open a passthrough window immediately for the given duration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ident := cfg
		resolved, err := discoverProjectFn()
		if err != nil {
			return err
		}
		projCfg, err := config.Load(resolved.MacCwd)
		if err != nil {
			return err
		}
		if _, err := daemonHandshake(cmd.Context(), ident, projCfg); err != nil {
			return err
		}
		durationSeconds, err := parsePassthroughDuration(args[0])
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()

		wasOpen, expiresSeconds, err := serviceapi.NewClient(ident).PassthroughEgress(ctx, projCfg.Project.Name, durationSeconds)
		if err != nil {
			return fmt.Errorf("passthrough egress: %w", err)
		}
		verb := "opened"
		if wasOpen {
			verb = "renewed"
		}
		fmt.Printf("egress PASSTHROUGH — %s for %s (auto-restores; run `devm passthrough close` to close early)\n",
			verb, (time.Duration(expiresSeconds) * time.Second).String())
		return nil
	},
}

var passthroughCloseCmd = &cobra.Command{
	Use:   "close",
	Short: "Close an active passthrough window immediately",
	Long: `Restores this project's egress policy to RESTRICTED, ending an
active ` + "`devm passthrough open`" + ` window before its timer fires.
No-op if no window is active.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ident := cfg
		resolved, err := discoverProjectFn()
		if err != nil {
			return err
		}
		projCfg, err := config.Load(resolved.MacCwd)
		if err != nil {
			return err
		}
		if _, err := daemonHandshake(cmd.Context(), ident, projCfg); err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()

		wasOpen, err := serviceapi.NewClient(ident).RestrictEgress(ctx, projCfg.Project.Name)
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

var passthroughApproveCmd = &cobra.Command{
	Use:   "approve",
	Short: "Approve a pending gdevm passthrough request",
	Long: `Consumes the project's pending guest-initiated passthrough
request (submitted by ` + "`gdevm passthrough --reason \"...\"`" + `)
and opens the egress window using that request's duration.

Errors if no pending request exists.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ident := cfg
		resolved, err := discoverProjectFn()
		if err != nil {
			return err
		}
		projCfg, err := config.Load(resolved.MacCwd)
		if err != nil {
			return err
		}
		if _, err := daemonHandshake(cmd.Context(), ident, projCfg); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()

		wasOpen, expiresSeconds, reason, err := serviceapi.NewClient(ident).ApprovePassthroughRequest(ctx, projCfg.Project.Name)
		if err != nil {
			return fmt.Errorf("passthrough approve: %w", err)
		}
		verb := "opened"
		if wasOpen {
			verb = "renewed"
		}
		if reason != "" {
			fmt.Printf("egress PASSTHROUGH — %s for %s (guest reason: %s)\n",
				verb, (time.Duration(expiresSeconds) * time.Second).String(), reason)
		} else {
			fmt.Printf("egress PASSTHROUGH — %s for %s\n",
				verb, (time.Duration(expiresSeconds) * time.Second).String())
		}
		return nil
	},
}

var passthroughDenyCmd = &cobra.Command{
	Use:   "deny",
	Short: "Deny a pending gdevm passthrough request",
	Long: `Clears the project's pending guest-initiated passthrough
request without opening a window.

Errors if no pending request exists.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ident := cfg
		resolved, err := discoverProjectFn()
		if err != nil {
			return err
		}
		projCfg, err := config.Load(resolved.MacCwd)
		if err != nil {
			return err
		}
		if _, err := daemonHandshake(cmd.Context(), ident, projCfg); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()

		reason, err := serviceapi.NewClient(ident).DenyPassthroughRequest(ctx, projCfg.Project.Name)
		if err != nil {
			return fmt.Errorf("passthrough deny: %w", err)
		}
		if reason != "" {
			fmt.Printf("passthrough request DENIED (guest reason: %s)\n", reason)
		} else {
			fmt.Println("passthrough request DENIED")
		}
		return nil
	},
}

func init() {
	passthroughCmd.AddCommand(passthroughOpenCmd, passthroughCloseCmd, passthroughApproveCmd, passthroughDenyCmd)
	rootCmd.AddCommand(passthroughCmd)
}
