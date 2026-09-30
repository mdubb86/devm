package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/spf13/cobra"
)

// popNativeFlag is the --native mode switch. When set, runPop uses
// the mirror-open + tart-exec-cp path instead of building a filestash
// URL. Package-level for test injection.
var popNativeFlag bool

// popExecOpen is the exec seam for macOS `open`. Tests override.
var popExecOpen = defaultPopExecOpen

func defaultPopExecOpen(args ...string) error {
	return exec.Command("open", args...).Run()
}

// tartExecCatFn is the seam for `tart exec <vm> cat <guest-path>`,
// output redirected to macDest. Tests override.
var tartExecCatFn = defaultTartExecCat

func defaultTartExecCat(vm, guestPath, macDest string) error {
	f, err := os.Create(macDest)
	if err != nil {
		return fmt.Errorf("create %s: %w", macDest, err)
	}
	defer f.Close()
	cmd := exec.Command("tart", "exec", vm, "cat", guestPath)
	cmd.Stdout = f
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Remove(macDest)
		return fmt.Errorf("tart exec cat %s: %w", guestPath, err)
	}
	return nil
}

var popCmd = &cobra.Command{
	Use:   "pop <path>",
	Short: "Open a file — filestash by default, --native for the macOS app",
	Long: `devm pop <path> opens <path> in a browser via the project's
bundled filestash service (https://files.<project>.<tld>). With
--native, it resolves <path> through the project's mirror table and
opens it in the macOS default app (Preview / Xcode / etc.); an
out-of-mirror <path> is cp'd from the guest via 'tart exec cat' first,
then opened.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runPop,
}

func init() {
	popCmd.Flags().BoolVar(&popNativeFlag, "native", false,
		"open in macOS default app (Preview, Xcode, etc.) instead of the browser")
	rootCmd.AddCommand(popCmd)
}

// errLegacyMacOrVmArg is returned when the first arg is the name of a
// removed `pop mac` / `pop vm` subcommand, so users following stale
// muscle memory get a clear migration error instead of devm silently
// trying (and failing) to pop a file literally named "mac" or "vm".
var errLegacyMacOrVmArg = errors.New(
	"pop: `pop mac` and `pop vm` subcommands have been removed; " +
		"use `devm pop [--native] <path>` instead")

func runPop(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if len(args) >= 2 && (args[0] == "mac" || args[0] == "vm") {
		return errLegacyMacOrVmArg
	}
	pathArg, openArgs := splitPathAndOpenArgs(args)

	// URL args pass through to `open` regardless of --native.
	if strings.HasPrefix(pathArg, "http://") || strings.HasPrefix(pathArg, "https://") {
		return popExecOpen(append([]string{pathArg}, openArgs...)...)
	}

	resolved, err := discoverProjectFn()
	if err != nil {
		return err
	}
	loaded, err := config.Load(resolved.MacCwd)
	if err != nil {
		return err
	}

	guestPath := resolveGuestPath(pathArg, resolved.MacCwd, loaded)

	if popNativeFlag {
		return runPopNative(pathArg, guestPath, resolved, loaded, openArgs)
	}
	return runPopDefault(guestPath, loaded, openArgs)
}

// runPopDefault builds the filestash URL for guestPath and opens it.
func runPopDefault(guestPath string, loaded schema.Config, openArgs []string) error {
	filestashURL := (&url.URL{
		Scheme: "https",
		Host:   "files." + loaded.Project.Name + "." + cfg.TLD,
		Path:   "/files/local" + guestPath,
	}).String()
	return popExecOpen(append([]string{filestashURL}, openArgs...)...)
}

// errNativeDir is returned whenever --native is asked to open a
// directory — tart exec cat is per-file, so the error names the
// default flow (filestash) as the working alternative.
var errNativeDir = errors.New(
	"pop: directories not supported under --native (tart exec cat is per-file); " +
		"drop --native to browse the directory in filestash's default flow")

// runPopNative implements the --native path: mirror-open for in-mirror
// paths, tart-exec-cp for out-of-mirror paths. Directories are refused
// with a clear error naming the alternative (default flow).
func runPopNative(userInput, guestPath string, resolved LocalProject, loaded schema.Config, openArgs []string) error {
	if strings.HasSuffix(userInput, "/") || strings.HasSuffix(guestPath, "/") {
		return errNativeDir
	}

	// Try the mirror-table resolution first.
	macPath, err := resolvePopTarget(userInput, resolved.MacCwd, loaded)
	if err == nil {
		if info, statErr := os.Stat(macPath); statErr == nil && info.IsDir() {
			return errNativeDir
		}
		return popExecOpen(append([]string{macPath}, openArgs...)...)
	}
	if !isOutOfMirrorErr(err) {
		return err
	}

	// Out-of-mirror: cp guest→scratch, then open the scratch file.
	scratchRoot := serviceapi.PopScratchRoot(cfg)
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
		return fmt.Errorf("pop --native: create scratch root %s: %w", scratchRoot, err)
	}
	dest := filepath.Join(scratchRoot, scratchName(loaded.Project.Name, guestPath))
	if err := tartExecCatFn(resolved.Name, guestPath, dest); err != nil {
		// tart exec cat on a directory fails with something like
		// "cat: <path>: Is a directory" — translate that into the
		// same clear, alternative-naming error as the other two
		// directory-detection paths above, rather than surfacing
		// cat's raw stderr.
		if strings.Contains(strings.ToLower(err.Error()), "is a directory") {
			return errNativeDir
		}
		return fmt.Errorf("pop --native: %w", err)
	}
	return popExecOpen(append([]string{dest}, openArgs...)...)
}

// scratchName is a stable name for the cp target — hash(project+path)
// so pops across different projects with the same basename don't
// collide, and repeated pops of the same path reuse the same file.
func scratchName(project, guestPath string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + guestPath))
	return hex.EncodeToString(sum[:])[:20] + filepath.Ext(guestPath)
}

// resolveGuestPath maps a user's pathArg to an absolute guest path.
// Relative → <guest-workspace-root>/<rel>, preserving a trailing
// slash so directory listing URLs stay directory-shaped. Absolute →
// passthrough (e.g. a path printed by a guest process).
func resolveGuestPath(pathArg string, macCwd string, loaded schema.Config) string {
	if filepath.IsAbs(pathArg) {
		return pathArg
	}
	// Guest workspace root is /home/devm/<primary-repo-label> — NOT
	// project.name. The label comes from the primary repo's explicit
	// `label:`, or is derived from its `url:`, or (repo-less primary)
	// the Mac cwd's basename — see serviceapi.PrimaryGuestPath, the
	// same resolution resolvePopTarget uses for --native. A
	// repo-less project (no repos at all) has no label; its guest
	// workspace root is /home/devm itself.
	root := serviceapi.PrimaryGuestPath(&loaded, macCwd)
	if root == "" {
		root = schema.GuestHomeDir
	}
	joined := filepath.Join(root, pathArg)
	if strings.HasSuffix(pathArg, "/") && !strings.HasSuffix(joined, "/") {
		joined += "/"
	}
	return joined
}

// isOutOfMirrorErr reports whether err is resolvePopTarget's
// not-in-any-mirror error, the one case runPopNative falls back to
// the tart-exec-cat scratch path instead of failing outright.
func isOutOfMirrorErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "is not inside any mirrored repo/volume")
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
