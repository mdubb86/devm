package repohelpers

import (
	"fmt"
	"path/filepath"
	"strings"
)

// WorkspacePathEntry is the minimal shape TranslateGuestPath needs
// from a workspace registry entry: where the project's clone lives
// inside the guest, and where its Mac-side volume storage lives.
// Callers holding a richer type (e.g. serviceapi.WorkspaceEntry)
// convert field-for-field before calling — repohelpers is a
// dependency of serviceapi, so it cannot import that type directly.
type WorkspacePathEntry struct {
	GuestPath   string
	StoragePath string
}

// TranslateGuestPath maps a guest-view absolute path (rooted in some
// workspace's GuestPath) to the corresponding Mac-side storage path.
// The registry is the daemon's workspace list — one entry per project
// with a primary repo. Returns an error naming the input if it isn't
// inside any known workspace.
//
// Prefix match is on the full path component boundary — a guest path
// like "/Users/x/projX/foo" does NOT match a workspace with GuestPath
// "/Users/x/proj".
func TranslateGuestPath(guestPath string, registry []WorkspacePathEntry) (string, error) {
	for _, w := range registry {
		if guestPath == w.GuestPath || strings.HasPrefix(guestPath, w.GuestPath+string(filepath.Separator)) {
			rel, err := filepath.Rel(w.GuestPath, guestPath)
			if err != nil {
				return "", fmt.Errorf("translate %s: %w", guestPath, err)
			}
			return filepath.Join(w.StoragePath, rel), nil
		}
	}
	return "", fmt.Errorf("%s is not inside any known devm workspace", guestPath)
}
