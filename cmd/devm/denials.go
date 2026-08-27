package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/repohelpers"
	"github.com/mdubb86/devm/internal/serviceapi"
)

var denialsJSON bool

var denialsCmd = &cobra.Command{
	Use:   "denials",
	Short: "Show hosts iron-proxy has rejected for this project",
	Long: `List every host iron-proxy has denied under the current project's
allow-list, with counts and last-seen timestamps. Sorted most-denied first.

Counts are held in daemon memory for the current iron-proxy process — they
reset when the sandbox is stopped or the daemon restarts. Use this to figure
out what to add to ` + "`network.allow`" + ` in devm.yaml when a tool inside the
sandbox is failing to reach an upstream.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		ident := cfg // capture package identity cfg before it's shadowed below
		cwd, err := os.Getwd()
		if err != nil {
			return err
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
		snap, err := serviceapi.NewClient(ident).Denials(ctx, cfg.Project.Name)
		if err != nil {
			return fmt.Errorf("query denials: %w", err)
		}
		if denialsJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(snap)
		}
		if len(snap) == 0 {
			fmt.Println("no denials recorded for this project")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "COUNT\tURL\tMETHOD\tLAST SEEN")
		now := time.Now().UTC()
		for _, d := range snap {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s ago\n",
				d.Count, urlFor(d.Host, d.Path), d.Method, humaniseDuration(now.Sub(d.LastSeen)))
		}
		return tw.Flush()
	},
}

// urlFor combines host and path into the readable form users would
// paste into `network.allow`. An empty path or a bare "/" collapses to
// just the host — that's a whole-host reject with no path signal to
// carry. Everything else prints as "host<path>" (path already begins
// with "/", so no separator needed).
func urlFor(host, path string) string {
	if path == "" || path == "/" {
		return host
	}
	return host + path
}

// humaniseDuration renders a compact "3m", "12s", "1h" for the LAST SEEN
// column. Negative or zero durations render as "just now" — clocks can
// skew slightly between the iron-proxy timestamp and the local Now().
func humaniseDuration(d time.Duration) string {
	if d <= 0 {
		return "just now"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func init() {
	denialsCmd.Flags().BoolVar(&denialsJSON, "json", false, "Emit JSON output")
	rootCmd.AddCommand(denialsCmd)
}
