package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/sandbox/tart"

	"github.com/spf13/cobra"
)

var (
	purgeYes    bool
	purgeDryRun bool
)

var purgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Delete state accumulated from projects that no longer exist",
	Long: `Scans ~/Library/Application Support/devm/ for per-project
mirror dirs (<projectID>/). For each, checks whether a tart VM or
state file still exists for the project. If neither: the project is
gone and its mirrored data is orphaned — a candidate for deletion.

Live projects are always skipped. Only fully-abandoned state gets
purged. Data-losing action, so the default is interactive
confirmation; --yes to skip, --dry-run to preview.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		// cfg is the package-level identity.Config set in main.go —
		// devm vs devm-e2e picks the right runtime dir automatically.
		lister := realVMLister{tart: tart.New()}
		return runPurge(cfg.RuntimeDir(), lister, purgeDryRun, purgeYes, os.Stdout)
	},
}

// vmLister is the interface runPurge depends on so tests can inject a fake.
type vmLister interface {
	List() ([]string, error)
}

// realVMLister wraps tart.New().List with a bounded timeout.
type realVMLister struct{ tart *tart.Tart }

func (r realVMLister) List() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	vms, err := r.tart.List(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(vms))
	for _, v := range vms {
		names = append(names, v.Name)
	}
	return names, nil
}

// purgeSkipDirs are devm-internal directory names directly under
// RuntimeDir() that are not project mirror dirs. Must agree with
// internal/schema.reservedProjectIDs — that validator is what stops a
// project.name from colliding with one of these in the first place,
// so the two lists describe the same set of reserved names.
var purgeSkipDirs = map[string]bool{
	"bin":         true, // devm-installed binaries (iron-proxy, mutagen, setsidshim, ...)
	"state":       true, // per-project state JSON
	"iron-proxy":  true, // per-project iron-proxy configs
	"mutagen":     true, // global mutagen daemon state + per-project session configs
	"ssh":         true, // per-project SSH key material for guest reach
	"secrets":     true, // file-backed secret store
	"ca":          true, // devm's root CA material
	"softnet-bin": true, // softnet binary + per-project sockets
	"volumes":     true, // legacy layout artifact
}

// runPurge is factored for testability. runtimeDir is
// ~/Library/Application Support/devm/ in production; tests pass a
// temp dir. lister returns the current tart VM names.
func runPurge(runtimeDir string, lister vmLister, dryRun, yes bool, out io.Writer) error {
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(out, "nothing to purge")
			return nil
		}
		return fmt.Errorf("read runtime dir: %w", err)
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "nothing to purge")
		return nil
	}

	vmNames, err := lister.List()
	if err != nil {
		return fmt.Errorf("list vms: %w", err)
	}
	vmSet := map[string]struct{}{}
	for _, n := range vmNames {
		vmSet[n] = struct{}{}
	}

	// Sort for deterministic output.
	var projects []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || purgeSkipDirs[name] {
			continue
		}
		projects = append(projects, name)
	}
	sort.Strings(projects)

	var candidates []string
	for _, p := range projects {
		if _, alive := vmSet[p]; alive {
			fmt.Fprintf(out, "skipped '%s': VM still exists\n", p)
			continue
		}
		statePath := filepath.Join(runtimeDir, "state", p+".json")
		if _, err := os.Stat(statePath); err == nil {
			fmt.Fprintf(out, "skipped '%s': state file exists\n", p)
			continue
		}
		candidates = append(candidates, p)
	}

	if len(candidates) == 0 {
		return nil
	}

	// Report candidates with sizes.
	for _, p := range candidates {
		dir := filepath.Join(runtimeDir, p)
		size := dirSize(dir) // shared with volume.go
		verb := "would delete"
		if !dryRun {
			verb = "candidate"
		}
		fmt.Fprintf(out, "%s '%s' (%s)\n", verb, p, humanBytes(size))
	}
	if dryRun {
		return nil
	}

	if !yes {
		fmt.Fprintf(out, "delete? [y/N]: ")
		var resp string
		_, _ = fmt.Fscanln(os.Stdin, &resp)
		if resp != "y" && resp != "Y" {
			fmt.Fprintln(out, "cancelled")
			return nil
		}
	}
	for _, p := range candidates {
		dir := filepath.Join(runtimeDir, p)
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(out, "failed to delete '%s': %v\n", p, err)
			continue
		}
		fmt.Fprintf(out, "deleted '%s'\n", p)
	}
	return nil
}

func init() {
	purgeCmd.Flags().BoolVar(&purgeYes, "yes", false, "Skip confirmation prompt")
	purgeCmd.Flags().BoolVar(&purgeDryRun, "dry-run", false, "Show what would be deleted without doing it")
	rootCmd.AddCommand(purgeCmd)
}
