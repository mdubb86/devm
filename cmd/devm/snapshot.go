package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/snapshot"
)

var (
	snapshotPruneKeep    int
	snapshotRestoreForce bool
)

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Capture, restore and prune per-entry snapshots",
	Long: `Drive the per-entry snapshot flows declared under
devm.yaml's snapshots: block. Each subcommand dispatches through the
daemon, which runs the backup / restore hook in the guest and manages
the Mac-side archive storage.`,
}

var snapshotCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Capture a snapshot for the named entry",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		client, project, err := snapshotProjectClient(cmd.Context())
		if err != nil {
			return err
		}
		return runSnapshotCreate(cmd.Context(), client, project, args[0], os.Stdout)
	},
}

var snapshotRestoreCmd = &cobra.Command{
	Use:   "restore <name> [<ts>]",
	Short: "Restore the named entry from a snapshot (defaults to latest)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		client, project, err := snapshotProjectClient(cmd.Context())
		if err != nil {
			return err
		}
		ts := ""
		if len(args) == 2 {
			ts = args[1]
		}
		return runSnapshotRestore(cmd.Context(), client, project, args[0], ts, snapshotRestoreForce, os.Stdout)
	},
}

var snapshotListCmd = &cobra.Command{
	Use:   "list [<name>]",
	Short: "List snapshot archives (all entries when <name> is omitted)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		client, project, err := snapshotProjectClient(cmd.Context())
		if err != nil {
			return err
		}
		entry := ""
		if len(args) == 1 {
			entry = args[0]
		}
		return runSnapshotList(cmd.Context(), client, project, entry, os.Stdout)
	},
}

var snapshotPruneCmd = &cobra.Command{
	Use:   "prune <name>",
	Short: "Remove older archives for the named entry, keeping the newest --keep",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		client, project, err := snapshotProjectClient(cmd.Context())
		if err != nil {
			return err
		}
		return runSnapshotPrune(cmd.Context(), client, project, args[0], snapshotPruneKeep, os.Stdout)
	},
}

var snapshotClearCmd = &cobra.Command{
	Use:   "clear <name>",
	Short: "Delete all archives for the named entry",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		client, project, err := snapshotProjectClient(cmd.Context())
		if err != nil {
			return err
		}
		return runSnapshotClear(cmd.Context(), client, project, args[0], os.Stdout)
	},
}

var snapshotShowCmd = &cobra.Command{
	Use:   "show <name> [<ts>]",
	Short: "Print one snapshot's metadata (defaults to latest)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		client, project, err := snapshotProjectClient(cmd.Context())
		if err != nil {
			return err
		}
		ts := ""
		if len(args) == 2 {
			ts = args[1]
		}
		return runSnapshotShow(cmd.Context(), client, project, args[0], ts, os.Stdout)
	},
}

// snapshotProjectClient runs the discover+load+handshake pattern every
// snapshot subcommand shares and returns the serviceapi client plus
// the resolved project name to send on the wire.
func snapshotProjectClient(ctx context.Context) (*serviceapi.Client, string, error) {
	ident := cfg
	resolved, err := discoverProjectFn()
	if err != nil {
		return nil, "", err
	}
	loaded, err := config.Load(resolved.MacCwd)
	if err != nil {
		return nil, "", err
	}
	if _, err := daemonHandshake(ctx, ident, loaded); err != nil {
		return nil, "", err
	}
	return serviceapi.NewClient(ident), loaded.Project.Name, nil
}

func runSnapshotCreate(ctx context.Context, c *serviceapi.Client, project, entry string, out io.Writer) error {
	meta, err := c.SnapshotCreate(ctx, project, entry, snapshot.SourceCLI)
	if err != nil {
		return fmt.Errorf("devm snapshot create: %s", err.Error())
	}
	fmt.Fprintf(out, "snapshot %s saved at %s (%s, %s)\n",
		entry, meta.TS, formatDuration(meta.DurationMs), formatSize(meta.SizeBytes))
	return nil
}

func runSnapshotRestore(ctx context.Context, c *serviceapi.Client, project, entry, ts string, force bool, out io.Writer) error {
	restored, err := c.SnapshotRestore(ctx, project, entry, ts, force)
	if err != nil {
		return fmt.Errorf("devm snapshot restore: %s", err.Error())
	}
	if !restored {
		fmt.Fprintf(os.Stderr, "no snapshot to restore for %s\n", entry)
		return nil
	}
	displayTS := ts
	if displayTS == "" {
		displayTS = "latest"
	}
	fmt.Fprintf(out, "restored snapshot %s from %s\n", entry, displayTS)
	return nil
}

func runSnapshotClear(ctx context.Context, c *serviceapi.Client, project, entry string, out io.Writer) error {
	removed, err := c.SnapshotClear(ctx, project, entry)
	if err != nil {
		return fmt.Errorf("devm snapshot clear: %s", err.Error())
	}
	fmt.Fprintf(out, "cleared %d archive(s) for %s\n", removed, entry)
	return nil
}

func runSnapshotList(ctx context.Context, c *serviceapi.Client, project, entry string, out io.Writer) error {
	metas, err := c.SnapshotList(ctx, project, entry)
	if err != nil {
		return fmt.Errorf("devm snapshot list: %s", err.Error())
	}
	if entry != "" {
		for _, m := range metas {
			fmt.Fprintf(out, "%s  %s  %s\n", m.TS, formatSize(m.SizeBytes), sourceLabel(m.Source))
		}
		return nil
	}
	// No entry — one row per declared entry plus any orphan dirs the
	// daemon found on disk. Tabulated so operators can scan mode and
	// status at a glance; the ORPHAN column only appears when at least
	// one row needs it.
	hasOrphan := false
	for _, m := range metas {
		if m.Orphan {
			hasOrphan = true
			break
		}
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	header := "NAME\tMODE\tSTATUS\tLATEST\tSIZE\tSOURCE"
	if hasOrphan {
		header += "\tORPHAN"
	}
	fmt.Fprintln(tw, header)
	for _, m := range metas {
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s",
			listName(m),
			string(m.EffectiveMode()),
			listStatus(m),
			formatTSHuman(m.TS),
			formatSize(m.SizeBytes),
			sourceLabel(m.Source),
		)
		if hasOrphan {
			cell := ""
			if m.Orphan {
				cell = "orphan"
			}
			row += "\t" + cell
		}
		fmt.Fprintln(tw, row)
	}
	return tw.Flush()
}

// listName picks the NAME column value for a row. ArchiveName is the
// entry name on fresh captures (and on the synthetic orphan rows the
// handler builds), but older sidecars stored the hook name — strip a
// "backup_" prefix so the column stays readable either way.
func listName(m snapshot.Meta) string {
	name := m.ArchiveName
	if name == "" {
		name = m.HookName
	}
	return strings.TrimPrefix(name, "backup_")
}

// listStatus returns the STATUS column value. Meta.Status is empty on
// v0.31 sidecars — surface it as "ok" rather than a blank cell so the
// column always reads as a decision.
func listStatus(m snapshot.Meta) string {
	if m.Status == "" {
		return string(snapshot.StatusOk)
	}
	return string(m.Status)
}

func runSnapshotPrune(ctx context.Context, c *serviceapi.Client, project, entry string, keep int, out io.Writer) error {
	removed, err := c.SnapshotPrune(ctx, project, entry, keep)
	if err != nil {
		return fmt.Errorf("devm snapshot prune: %s", err.Error())
	}
	fmt.Fprintf(out, "pruned %d snapshot(s) for %s\n", len(removed), entry)
	for _, ts := range removed {
		fmt.Fprintf(out, "  %s\n", ts)
	}
	return nil
}

func runSnapshotShow(ctx context.Context, c *serviceapi.Client, project, entry, ts string, out io.Writer) error {
	meta, err := c.SnapshotShow(ctx, project, entry, ts)
	if err != nil {
		return fmt.Errorf("devm snapshot show: %s", err.Error())
	}
	fmt.Fprintf(out, "ts:            %s\n", meta.TS)
	fmt.Fprintf(out, "hook_name:     %s\n", meta.HookName)
	fmt.Fprintf(out, "archive_name:  %s\n", meta.ArchiveName)
	fmt.Fprintf(out, "source:        %s\n", sourceLabel(meta.Source))
	fmt.Fprintf(out, "mode:          %s\n", meta.EffectiveMode())
	fmt.Fprintf(out, "status:        %s\n", listStatus(meta))
	if meta.CapturedPath != "" {
		fmt.Fprintf(out, "captured_path: %s\n", meta.CapturedPath)
		fmt.Fprintf(out, "is_dir:        %t\n", meta.IsDir)
	}
	if meta.Orphan {
		fmt.Fprintf(out, "orphan:        true\n")
	}
	fmt.Fprintf(out, "exit_code:     %d\n", meta.ExitCode)
	fmt.Fprintf(out, "duration:      %s\n", formatDuration(meta.DurationMs))
	fmt.Fprintf(out, "size:          %s (%d bytes)\n", formatSize(meta.SizeBytes), meta.SizeBytes)
	return nil
}

// formatSize renders bytes as a base-1024 string with one fractional
// digit, rolling up through KiB / MiB / GiB. Zero bytes prints as "0 B".
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	f := float64(bytes)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	i := -1
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// formatDuration renders a millisecond duration using Go's native
// time.Duration.String(), which already gives "2.4s" / "150ms" shapes.
func formatDuration(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).String()
}

// formatTSHuman turns a snapshot TS like "2026-11-16T0300-00" into the
// "YYYY-MM-DD HH:MM" shape the list-without-entry view wants. Falls back
// to the raw TS when the layout does not parse — never a value worth
// hiding from the operator.
func formatTSHuman(ts string) string {
	if ts == "" {
		return "<never>"
	}
	// Match snapshot.Capture's timestamp layout (see internal/snapshot).
	t, err := time.Parse("2006-01-02T1504-05", ts)
	if err != nil {
		return ts
	}
	return t.Format("2006-01-02 15:04")
}

// sourceLabel returns a non-empty label for a Meta.Source. An archive
// that lost its sidecar comes back from the daemon as SourceUnknown;
// surface that explicitly rather than printing an empty column.
func sourceLabel(s snapshot.Source) string {
	if s == "" {
		return string(snapshot.SourceUnknown)
	}
	return strings.TrimSpace(string(s))
}

func init() {
	snapshotPruneCmd.Flags().IntVar(&snapshotPruneKeep, "keep", 0,
		"How many newest archives to retain (required)")
	snapshotRestoreCmd.Flags().BoolVar(&snapshotRestoreForce, "force", false,
		"Bypass mode/path drift refusals when archive and yaml disagree")
	snapshotCmd.AddCommand(snapshotCreateCmd)
	snapshotCmd.AddCommand(snapshotRestoreCmd)
	snapshotCmd.AddCommand(snapshotListCmd)
	snapshotCmd.AddCommand(snapshotPruneCmd)
	snapshotCmd.AddCommand(snapshotShowCmd)
	snapshotCmd.AddCommand(snapshotClearCmd)
	rootCmd.AddCommand(snapshotCmd)
}
