package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/repohelpers"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
)

var popCmd = &cobra.Command{
	Use:   "pop",
	Short: "Open a file on the Mac with its default app",
	Long: `devm pop opens a file on the Mac using macOS 'open'. The 'mac'
subcommand opens a Mac-native file; the 'vm' subcommand opens a file
that lives in the project's VM-view (volume storage) via its .vm/
symlink.

The mac/vm split is explicit — the same path string can refer to
different files depending on scope, and pop refuses to guess.`,
}

var popMacCmd = &cobra.Command{
	Use:   "mac <path> [-- <open-args>...]",
	Short: "Open a Mac-native file (refuses paths that resolve into a .vm/-managed volume)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		pathArg, openArgs := splitPathAndOpenArgs(args)
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		registry, err := serviceapi.NewClient(cfg).Workspaces(cmd.Context())
		if err != nil {
			return fmt.Errorf("query daemon: %w", err)
		}
		resolved, err := resolveMacMode(pathArg, cwd, registry)
		if err != nil {
			return err
		}
		return exec.Command("open", append([]string{resolved}, openArgs...)...).Run()
	},
}

var popVMCmd = &cobra.Command{
	Use:   "vm <path> [-- <open-args>...]",
	Short: "Open a file that lives in the project's VM-view (volume storage)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		pathArg, openArgs := splitPathAndOpenArgs(args)
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		repoRoot, err := repohelpers.FindDevmYAML(cwd)
		if err != nil {
			return err
		}
		registry, err := serviceapi.NewClient(cfg).Workspaces(cmd.Context())
		if err != nil {
			return fmt.Errorf("query daemon: %w", err)
		}
		resolved, err := resolveVMMode(pathArg, repoRoot, registry)
		if err != nil {
			return err
		}
		return exec.Command("open", append([]string{resolved}, openArgs...)...).Run()
	},
}

// splitPathAndOpenArgs splits `<path> [-- <open-args>...]` into the
// path and forwarded open args.
func splitPathAndOpenArgs(args []string) (pathArg string, openArgs []string) {
	pathArg = args[0]
	for i, a := range args[1:] {
		if a == "--" {
			openArgs = args[1+i+1:]
			return
		}
	}
	return
}

// resolveMacMode implements pop mac's resolution: cwd-first then
// project-root fallback, with refusal if the resolved candidate's real
// inode is inside any devm workspace's StoragePath.
func resolveMacMode(pathArg, cwd string, registry []serviceapi.WorkspaceEntry) (string, error) {
	var candidates []string
	if filepath.IsAbs(pathArg) {
		candidates = []string{pathArg}
	} else {
		candidates = []string{filepath.Join(cwd, pathArg)}
		root, err := repohelpers.FindDevmYAML(cwd)
		if err == nil {
			projectCand := filepath.Join(root, pathArg)
			if projectCand != candidates[0] {
				candidates = append(candidates, projectCand)
			}
		}
	}

	for _, cand := range candidates {
		if _, err := os.Stat(cand); err != nil {
			continue
		}
		// EvalSymlinks tells us the real inode. Refuse if it's inside
		// any workspace's StoragePath — that's a .vm/-managed volume
		// file and needs pop vm.
		real, err := filepath.EvalSymlinks(cand)
		if err != nil {
			return "", fmt.Errorf("pop: EvalSymlinks %s: %w", cand, err)
		}
		for _, w := range registry {
			// Resolve the workspace's StoragePath too — on macOS
			// t.TempDir()-style paths (and /var itself) are commonly a
			// symlink, so comparing against the real inode requires
			// both sides evaluated the same way.
			realStorage, err := filepath.EvalSymlinks(w.StoragePath)
			if err != nil {
				continue
			}
			if real == realStorage || strings.HasPrefix(real, realStorage+string(filepath.Separator)) {
				return "", fmt.Errorf("pop: %s resolves into a devm-managed volume (via .vm/); use `devm pop vm` instead", cand)
			}
		}
		return cand, nil
	}

	return "", fmt.Errorf("pop: no such file %q relative to %s or project root", pathArg, cwd)
}

// resolveVMMode implements pop vm's resolution: always project-root-
// relative on the Mac side (cwd doesn't map cleanly to VM cwd). Returns
// the pretty .vm/-form Mac path if the file exists in volume storage.
func resolveVMMode(pathArg, repoRoot string, registry []serviceapi.WorkspaceEntry) (string, error) {
	var guestPath string
	if filepath.IsAbs(pathArg) {
		guestPath = pathArg
	} else {
		// Find the workspace entry whose GuestPath matches repoRoot.
		var entry *serviceapi.WorkspaceEntry
		for i := range registry {
			if registry[i].GuestPath == repoRoot {
				entry = &registry[i]
				break
			}
		}
		if entry == nil {
			return "", fmt.Errorf("pop: no workspace registered for project root %s", repoRoot)
		}
		guestPath = filepath.Join(entry.GuestPath, pathArg)
	}

	pathEntries := make([]repohelpers.WorkspacePathEntry, len(registry))
	for i, w := range registry {
		pathEntries[i] = repohelpers.WorkspacePathEntry{GuestPath: w.GuestPath, StoragePath: w.StoragePath}
	}
	storagePath, err := repohelpers.TranslateGuestPath(guestPath, pathEntries)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(storagePath); err != nil {
		return "", fmt.Errorf("pop: no such file %q in VM project root", pathArg)
	}

	// Convert to pretty .vm/-form.
	for _, w := range registry {
		if storagePath == w.StoragePath || strings.HasPrefix(storagePath, w.StoragePath+string(filepath.Separator)) {
			rel, err := filepath.Rel(w.StoragePath, storagePath)
			if err != nil {
				return "", err
			}
			return filepath.Join(w.GuestPath, ".vm", rel), nil
		}
	}
	// Unreachable: storagePath came from TranslateGuestPath against the
	// same registry, so some entry's StoragePath must match.
	return "", fmt.Errorf("pop: internal error building pretty path for %s", storagePath)
}

func init() {
	popCmd.AddCommand(popMacCmd)
	popCmd.AddCommand(popVMCmd)
	rootCmd.AddCommand(popCmd)
}
