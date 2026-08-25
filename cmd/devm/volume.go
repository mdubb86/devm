package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/repohelpers"
	"github.com/mdubb86/devm/internal/schema"

	"github.com/spf13/cobra"
)

var volumeCmd = &cobra.Command{
	Use:   "volume",
	Short: "Manage per-project persistent volumes",
}

var volumeLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List this project's volumes (name, guest path, Mac path, size)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		repoRoot, err := repohelpers.FindDevmYAML(cwd)
		if err != nil {
			return err
		}
		userCfg, err := config.Load(repoRoot)
		if err != nil {
			return fmt.Errorf("locate devm.yaml: %w (run `devm volume ls` from a project root)", err)
		}
		// cfg is the package-level identity.Config set by
		// identity.Load() in main.go — resolves to identity.Prod for
		// the shipped devm binary and identity.E2E for devm-e2e.
		return runVolumeLs(cfg, userCfg, repoRoot, os.Stdout)
	},
}

// runVolumeLs is factored out for testability. ident tells us which
// daemon's runtime dir to look under (devm vs devm-e2e); tests hand
// in a synthetic identity with a temp-dir-resolving RuntimeDir. cwd is
// the project root (Mac cwd), needed to derive the auto-managed
// primary workspace volume's name and guest path when `repo:` is
// configured. Writes to `out`.
func runVolumeLs(ident identity.Config, userCfg schema.Config, cwd string, out io.Writer) error {
	volumesRoot := filepath.Join(ident.RuntimeDir(), "volumes", userCfg.Project.Name)

	type row struct{ name, guestPath, macPath string }
	var rows []row
	if userCfg.Repo != nil {
		primary := repohelpers.PrimaryVolumeName(cwd)
		rows = append(rows, row{primary, cwd, filepath.Join(volumesRoot, primary)})
	}
	for name, v := range userCfg.Volumes {
		rows = append(rows, row{name, v.Path, filepath.Join(volumesRoot, name)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tGUEST PATH\tMAC PATH\tSIZE")
	for _, r := range rows {
		size := dirSize(r.macPath)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.name, r.guestPath, r.macPath, humanBytes(size))
	}
	return tw.Flush()
}

// dirSize returns the total byte count of everything under path,
// treating a missing path as 0. Symlinks are not followed; a
// symlinked file contributes 0 bytes. Failure on any file is silently
// skipped so a permissions oddity doesn't mask the rest of the tree.
func dirSize(path string) int64 {
	var total int64
	filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// humanBytes renders a byte count with a single-letter SI suffix
// (B/K/M/G/T). Uses 1024, matching `du -h`.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}

func init() {
	volumeCmd.AddCommand(volumeLsCmd)
	rootCmd.AddCommand(volumeCmd)
}
