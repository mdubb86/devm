package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/repohelpers"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
)

// popExecOpen is the exec seam for tests to override macOS `open`.
var popExecOpen = func(args ...string) error {
	return exec.Command("open", args...).Run()
}

var popCmd = &cobra.Command{
	Use:   "pop",
	Short: "Open a file with its default Mac app",
	Long: `devm pop resolves <path> through the project's label→mirror
table and opens the resulting Mac-side file with macOS 'open'. <path>
may be an absolute guest path (e.g. one printed by a guest process, or
a project-root-relative path).

The 'mac' and 'vm' subcommands are equivalent — both resolve the same
way.`,
}

var popMacCmd = &cobra.Command{
	Use:   "mac <path> [-- <open-args>...]",
	Short: "Open a file, resolving it through the project's mirror table",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runPop,
}

var popVMCmd = &cobra.Command{
	Use:   "vm <path> [-- <open-args>...]",
	Short: "Open a file, resolving it through the project's mirror table",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runPop,
}

func runPop(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	pathArg, openArgs := splitPathAndOpenArgs(args)

	// URL arg: pass straight to `open`, which routes it to the default
	// browser (or the appropriate handler for the scheme). No config
	// load, no mirror-table walk — the URL IS the resource.
	if strings.HasPrefix(pathArg, "http://") || strings.HasPrefix(pathArg, "https://") {
		return popExecOpen(append([]string{pathArg}, openArgs...)...)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repoRoot, err := repohelpers.FindDevmYAML(cwd)
	if err != nil {
		return err
	}
	loaded, err := config.Load(repoRoot)
	if err != nil {
		return err
	}

	resolved, err := resolvePopTarget(pathArg, repoRoot, loaded)
	if err != nil {
		return err
	}
	return popExecOpen(append([]string{resolved}, openArgs...)...)
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

// resolvePopTarget resolves pathArg to the Mac-side mirror path via
// pcfg's label→mirror table (the same one cp's mountPassthrough
// walks). An absolute pathArg is treated as a guest path directly
// (e.g. one printed by a guest process); a relative pathArg is
// resolved against the primary repo's guest tree — repoRoot itself
// isn't kept in sync with the mirror, so a relative arg is always
// project-root-relative, never cwd-relative.
func resolvePopTarget(pathArg, repoRoot string, pcfg schema.Config) (string, error) {
	projectName := pcfg.Project.Name

	var guestPath string
	if filepath.IsAbs(pathArg) {
		guestPath = pathArg
	} else {
		primaryGuestPath := serviceapi.PrimaryGuestPath(&pcfg, repoRoot)
		if primaryGuestPath == "" {
			return "", fmt.Errorf("pop: no primary repo configured for project root %s", repoRoot)
		}
		guestPath = filepath.Join(primaryGuestPath, pathArg)
	}

	storagePath, ok := mountPassthrough(guestPath, repoRoot, pcfg, projectName)
	if !ok {
		return "", fmt.Errorf("pop: %q is not inside any mirrored repo/volume for this project", pathArg)
	}
	if _, err := os.Stat(storagePath); err != nil {
		return "", fmt.Errorf("pop: no such file %q in project mirror", pathArg)
	}
	return storagePath, nil
}

func init() {
	popCmd.AddCommand(popMacCmd)
	popCmd.AddCommand(popVMCmd)
	rootCmd.AddCommand(popCmd)
}
