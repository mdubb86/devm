package main

import (
	"fmt"
	"os"

	"github.com/mdubb86/devm/internal/config"
	"github.com/mdubb86/devm/internal/repohelpers"
)

// LocalProject is what discoverProject returns: the Mac-side project
// root (containing devm.yaml) and the daemon's canonical project name.
// Downstream code that needs the project root as a Mac-side path
// (mount/label resolution, template rendering, volume paths) uses
// MacCwd, never a fresh os.Getwd() — the caller may be in a subdir.
type LocalProject struct {
	MacCwd string
	Name   string
}

// discoverProjectFn is the test seam: real callers use discoverProject,
// tests override to bypass the filesystem walk.
var discoverProjectFn = discoverProject

// discoverProject walks up os.Getwd() for a directory containing
// devm.yaml, parses project.name, and returns {MacCwd, Name}. Purely
// local — no daemon roundtrip.
func discoverProject() (LocalProject, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return LocalProject{}, fmt.Errorf("discover-project: getcwd: %w", err)
	}
	return discoverProjectFromCwd(cwd)
}

func discoverProjectFromCwd(cwd string) (LocalProject, error) {
	root, err := repohelpers.FindDevmYAML(cwd)
	if err != nil {
		return LocalProject{}, err
	}
	name, err := config.ReadProjectName(root)
	if err != nil {
		return LocalProject{}, err
	}
	return LocalProject{MacCwd: root, Name: name}, nil
}
