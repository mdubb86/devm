// Package release supports the devm upgrade + version subcommands.
// install_source.go classifies how the running binary was installed
// so devm upgrade can refuse self-updates for brew-managed binaries.
package release

import "strings"

// Source identifies how a devm binary was installed.
type Source int

const (
	// SourceManual covers curl install, go install, manual download,
	// and anything else that isn't a known brew prefix.
	SourceManual Source = iota
	// SourceBrew covers Homebrew installs (any standard Cellar prefix).
	SourceBrew
)

// String returns "brew" or "manual".
func (s Source) String() string {
	if s == SourceBrew {
		return "brew"
	}
	return "manual"
}

// brewPrefixes are the Cellar paths under which Homebrew installs
// formulas. Matching is by string prefix on a resolved (symlinks
// followed) executable path. Order doesn't matter; any match wins.
var brewPrefixes = []string{
	"/opt/homebrew/Cellar/",              // Apple Silicon
	"/usr/local/Cellar/",                 // Intel Mac
	"/home/linuxbrew/.linuxbrew/Cellar/", // Linuxbrew
}

// Classify returns SourceBrew if execPath is under a known brew
// Cellar prefix, SourceManual otherwise. execPath should be the
// resolved (via os.Executable() + filepath.EvalSymlinks) path of
// the running binary.
func Classify(execPath string) Source {
	for _, p := range brewPrefixes {
		if strings.HasPrefix(execPath, p) {
			return SourceBrew
		}
	}
	return SourceManual
}
