// Package release supports the devm upgrade + version subcommands.
// install_source.go classifies whether the running binary was installed
// via Homebrew so devm upgrade can refuse self-updates that would
// otherwise create a brew/binary version mismatch.
package release

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Source identifies how a devm binary was installed.
type Source int

const (
	// SourceManual covers curl install, manual download, dev builds via
	// `go install`, copies of a brew binary to non-brew paths, and
	// anything else not actively managed by Homebrew.
	SourceManual Source = iota
	// SourceBrew covers binaries Homebrew owns at their canonical
	// install path. Determined authoritatively by asking brew, not by
	// path-prefix guessing.
	SourceBrew
)

// String returns "brew" or "manual".
func (s Source) String() string {
	if s == SourceBrew {
		return "brew"
	}
	return "manual"
}

// BrewLister is the seam Classify uses to ask Homebrew which files it
// owns under a given scope (`--cask` or `--formula`). The default
// implementation shells out to `brew list <scope> devm`. Tests inject
// a stub returning canned paths.
//
// A nil BrewLister means brew is not available; Classify returns
// SourceManual without further work.
type BrewLister func(ctx context.Context, scope, name string) (paths []string, err error)

// Classify reports whether the running binary at execPath is managed
// by Homebrew. execPath must be the resolved (symlinks-followed)
// absolute path of the binary — the caller does the resolution
// (os.Executable + filepath.EvalSymlinks) before calling.
//
// The check is authoritative: Classify asks brew which files it owns
// under both --cask and --formula scopes for the package name "devm",
// then looks for an exact match against execPath. This handles the
// custom-prefix case (Homebrew installed under a non-standard root),
// the cask-vs-formula distinction (we ship a cask today, but might
// also be installed as a formula somewhere), and the "copied to a
// manual path" case (a brew binary copied elsewhere is no longer
// brew-managed — copies are self-updateable).
func Classify(ctx context.Context, execPath string, lister BrewLister) Source {
	if lister == nil {
		return SourceManual
	}
	for _, scope := range []string{"--cask", "--formula"} {
		paths, err := lister(ctx, scope, "devm")
		if err != nil {
			continue
		}
		for _, p := range paths {
			if p == execPath {
				return SourceBrew
			}
		}
	}
	return SourceManual
}

// DefaultBrewLister returns a BrewLister that shells out to the real
// `brew` binary, or nil if `brew` is not on PATH. Callers should
// pass the result directly to Classify — Classify handles the nil
// case as "brew not available, classify as manual."
//
// The shell-out uses a 3-second per-call context timeout. Healthy
// brew returns in under 200ms; the budget exists so a wedged brew
// can't stall `devm version` indefinitely.
func DefaultBrewLister() BrewLister {
	if _, err := exec.LookPath("brew"); err != nil {
		return nil
	}
	return func(ctx context.Context, scope, name string) ([]string, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "brew", "list", scope, name).Output()
		if err != nil {
			return nil, err
		}
		return parseBrewListOutput(out), nil
	}
}

// parseBrewListOutput splits brew's stdout (one path per line) into a
// slice of trimmed paths, dropping empty lines. Split out so the parse
// behavior can be unit-tested without exec'ing anything.
func parseBrewListOutput(out []byte) []string {
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}
