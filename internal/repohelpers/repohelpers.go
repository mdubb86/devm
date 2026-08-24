package repohelpers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DeriveRepoURL returns the `origin` remote URL for the git repo at
// macCwd. Errors clearly when macCwd isn't a git working tree or has
// no origin remote; both errors name the fixes so a user can act
// without reading source.
func DeriveRepoURL(macCwd string) (string, error) {
	// Presence check first — surfaces a clearer error than git's raw
	// "not a git repository" for the common typo/mistake.
	if _, err := os.Stat(filepath.Join(macCwd, ".git")); err != nil {
		return "", fmt.Errorf(
			"%s is not a git repository: run inside a git repo checkout, or add `repo.url:` explicitly to devm.yaml",
			macCwd)
	}
	cmd := exec.Command("git", "-C", macCwd, "remote", "get-url", "origin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "No such remote") {
			return "", fmt.Errorf(
				"no `origin` remote in %s: add `repo.url:` explicitly to devm.yaml, or `git remote add origin <url>`",
				macCwd)
		}
		return "", fmt.Errorf("git remote get-url origin: %s", msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// PrimaryVolumeName returns the primary volume name for a project
// whose Mac cwd is macCwd. Per the design, it is simply the folder
// basename of Mac cwd (so /Users/me/projects/sewtrue → "sewtrue").
func PrimaryVolumeName(macCwd string) string {
	return filepath.Base(filepath.Clean(macCwd))
}
