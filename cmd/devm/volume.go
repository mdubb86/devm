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
	Short: "List this project's repos and volumes (name, label, kind, guest path, Mac path, size)",
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

// guestHomeDir is the guest-side parent for every repo clone — mirrors
// serviceapi.guestHomeDir (unexported there; the CLI needs its own
// copy since it resolves labels itself rather than shelling out to
// git via serviceapi.BuildEntities, see resolveRepoLabel).
const guestHomeDir = "/home/devm"

// findPrimaryRepoName returns the name of repos' primary entry: the
// one explicitly marked Primary, or else the sole entry with a nil
// URL. Mirrors schema's validateRepos primary-determination and
// serviceapi.findPrimaryRepoName; callers are expected to hand it an
// already-validated Config. Returns "" if no entry qualifies.
func findPrimaryRepoName(repos map[string]schema.RepoConfig) string {
	var urlNilName string
	urlNilCount := 0
	for name, r := range repos {
		if r.Primary != nil && *r.Primary {
			return name
		}
		if r.URL == nil {
			urlNilName = name
			urlNilCount++
		}
	}
	if urlNilCount == 1 {
		return urlNilName
	}
	return ""
}

// resolveRepoLabel resolves one repos.<name> entry's mutagen sync
// label: an explicit `label:` always wins; else a repo with a URL
// uses schema.BareCloneName; else (the URL-nil primary) the basename
// of macCwd. Mirrors serviceapi.resolveRepoLabel.
func resolveRepoLabel(r schema.RepoConfig, macCwd string) string {
	if r.Label != nil {
		return *r.Label
	}
	if r.URL != nil {
		return schema.BareCloneName(*r.URL)
	}
	return filepath.Base(macCwd)
}

// resolveVolumeLabel resolves one volumes.<name> entry's mutagen sync
// label: an explicit `label:` always wins; else the leaf dir of Path.
// Mirrors serviceapi.resolveVolumeLabel.
func resolveVolumeLabel(v schema.Volume) string {
	if v.Label != nil {
		return *v.Label
	}
	return filepath.Base(v.Path)
}

// volumeLsRow is one printed line: a repos or volumes entry. macPath
// is "" for a repo that isn't mirrored (a secondary without
// `volume: true`) — no mutagen session, no Mac-side storage to size.
type volumeLsRow struct {
	name, label, kind, guestPath, macPath string
}

// runVolumeLs is factored out for testability. ident tells us which
// daemon's runtime dir to look under (devm vs devm-e2e); tests hand
// in a synthetic identity with a temp-dir-resolving RuntimeDir. cwd is
// the project root (Mac cwd), needed to resolve the label of a
// URL-nil primary repo. Writes to `out`.
func runVolumeLs(ident identity.Config, userCfg schema.Config, cwd string, out io.Writer) error {
	projectID := userCfg.Project.Name
	mirrorPath := func(label string) string {
		return filepath.Join(ident.RuntimeDir(), projectID, label)
	}

	// seen guards the flat label namespace repos: and volumes: share —
	// two entries resolving to the same label would collide on the
	// same Mac mirror dir. Config.Validate already rejects this at
	// load time; this is a defensive re-check for callers (tests, or a
	// future caller) that hand runVolumeLs an unvalidated Config.
	seen := map[string]string{} // label -> "kind.name"
	claim := func(kind, name, label string) error {
		owner := kind + "." + name
		if prior, ok := seen[label]; ok {
			return fmt.Errorf("label %q: %s and %s both resolve to it — set an explicit `label:` on one", label, prior, owner)
		}
		seen[label] = owner
		return nil
	}

	var rows []volumeLsRow

	primaryName := findPrimaryRepoName(userCfg.Repos)
	repoNames := make([]string, 0, len(userCfg.Repos))
	for name := range userCfg.Repos {
		repoNames = append(repoNames, name)
	}
	sort.Strings(repoNames)
	for _, name := range repoNames {
		r := userCfg.Repos[name]
		isPrimary := name == primaryName
		var mirrored bool
		if isPrimary {
			mirrored = r.Volume == nil || *r.Volume
		} else {
			mirrored = r.Volume != nil && *r.Volume
		}

		label := resolveRepoLabel(r, cwd)
		if err := claim("repos", name, label); err != nil {
			return err
		}

		macPath := ""
		if mirrored {
			macPath = mirrorPath(label)
		}
		rows = append(rows, volumeLsRow{
			name:      name,
			label:     label,
			kind:      "repo",
			guestPath: filepath.Join(guestHomeDir, label),
			macPath:   macPath,
		})
	}

	volNames := make([]string, 0, len(userCfg.Volumes))
	for name := range userCfg.Volumes {
		volNames = append(volNames, name)
	}
	sort.Strings(volNames)
	for _, name := range volNames {
		v := userCfg.Volumes[name]
		label := resolveVolumeLabel(v)
		if err := claim("volumes", name, label); err != nil {
			return err
		}
		rows = append(rows, volumeLsRow{
			name:      name,
			label:     label,
			kind:      "volume",
			guestPath: v.Path,
			macPath:   mirrorPath(label),
		})
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLABEL\tKIND\tGUEST PATH\tMAC PATH\tSIZE")
	for _, r := range rows {
		size := "-"
		if r.macPath != "" {
			size = humanBytes(dirSize(r.macPath))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.name, r.label, r.kind, r.guestPath, r.macPath, size)
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
