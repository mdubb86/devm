package serviceapi

import (
	"errors"
	"fmt"
	"os"

	"github.com/mdubb86/devm/internal/identity"
)

// ResolverFileState classifies what's currently at cfg.ResolverFilePath.
type ResolverFileState int

const (
	ResolverFileMissing  ResolverFileState = iota // file doesn't exist
	ResolverFileMatches                           // file equals canonical
	ResolverFileDiverged                          // file exists but differs
)

// CheckResolverFile reads cfg.ResolverFilePath (no sudo needed) and
// reports its state.
func CheckResolverFile(cfg identity.Config) (ResolverFileState, error) {
	return checkResolverFileAt(cfg, cfg.ResolverFilePath)
}

func checkResolverFileAt(cfg identity.Config, path string) (ResolverFileState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ResolverFileMissing, nil
		}
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	if string(data) == cfg.CanonicalResolverContents() {
		return ResolverFileMatches, nil
	}
	return ResolverFileDiverged, nil
}
