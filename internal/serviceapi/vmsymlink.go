package serviceapi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const vmSymlinkName = ".vm"
const gitExcludeEntry = "/.vm\n"

// EnsureVMSymlink creates or refreshes <macCwd>/.vm as a symlink to
// primaryStoragePath. Idempotent, self-healing: a correct symlink is
// left alone, a stale one is replaced, a missing one is (re)created.
// Refuses to touch .vm if it exists as a non-symlink file or dir.
func EnsureVMSymlink(macCwd, primaryStoragePath string) error {
	linkPath := filepath.Join(macCwd, vmSymlinkName)
	current, err := os.Readlink(linkPath)
	if err == nil && current == primaryStoragePath {
		return nil // already correct
	}
	if err == nil {
		// Points elsewhere — replace.
		if rmErr := os.Remove(linkPath); rmErr != nil {
			return fmt.Errorf("remove stale .vm symlink: %w", rmErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		// Readlink fails with something other than ErrNotExist when the
		// path exists but isn't a symlink (or can't be read). Refuse to
		// touch a real file/dir left by the user.
		if fi, statErr := os.Lstat(linkPath); statErr == nil && fi.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf(".vm exists in %s but is not a symlink; leaving alone", macCwd)
		}
	}
	if err := os.Symlink(primaryStoragePath, linkPath); err != nil {
		return fmt.Errorf("create .vm symlink: %w", err)
	}
	return nil
}

// EnsureGitExclude appends `/.vm\n` to .git/info/exclude iff not already
// present. Silent no-op when .git/info/exclude doesn't exist (not a git
// repo) — the symlink still works, just without gitignore machinery.
func EnsureGitExclude(macCwd string) error {
	excludePath := filepath.Join(macCwd, ".git", "info", "exclude")
	body, err := os.ReadFile(excludePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil // not a git repo — nothing to append to
	}
	if err != nil {
		return fmt.Errorf("read .git/info/exclude: %w", err)
	}
	if strings.Contains(string(body), gitExcludeEntry) {
		return nil
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open .git/info/exclude for append: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(gitExcludeEntry); err != nil {
		return fmt.Errorf("append to .git/info/exclude: %w", err)
	}
	return nil
}
