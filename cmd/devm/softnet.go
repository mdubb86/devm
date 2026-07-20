package main

import (
	"os"
	"path/filepath"

	"github.com/mdubb86/devm/internal/softnet"
)

// IsSoftnetInvocation reports whether this process was exec'd as `softnet`
// (tart resolves a `softnet`-named symlink to the devm binary on $PATH).
func IsSoftnetInvocation(argv0 string) bool {
	return filepath.Base(argv0) == "softnet"
}

// runSoftnetAndExit runs softnet mode and never returns.
func runSoftnetAndExit() {
	if err := softnet.Run(cfg, os.Args[1:]); err != nil {
		// stderr, non-zero — the daemon/test reads the exit code.
		os.Stderr.WriteString("softnet: " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(0)
}
