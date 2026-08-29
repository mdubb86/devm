// run is the guest-side task dispatcher. Reads /opt/devm/commands.json,
// walks up from $PWD to find its containing repo, and execs the named
// command inside that repo's guestPath with bash -c.
//
// See docs/superpowers/specs/2026-08-29-repo-commands.md for the design.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const defaultManifest = "/opt/devm/commands.json"

type manifest struct {
	Repos map[string]struct {
		GuestPath string `json:"guestPath"`
		Commands  map[string]struct {
			Exec    string `json:"exec"`
			Startup bool   `json:"startup"`
		} `json:"commands"`
	} `json:"repos"`
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "" {
		fmt.Fprintln(os.Stderr, "run: usage: run <command>")
		os.Exit(2)
	}
	name := os.Args[1]

	manifestPath := os.Getenv("DEVM_COMMANDS_MANIFEST")
	if manifestPath == "" {
		manifestPath = defaultManifest
	}
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: cannot read manifest %s: %v\n", manifestPath, err)
		os.Exit(1)
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		fmt.Fprintf(os.Stderr, "run: parse manifest: %v\n", err)
		os.Exit(1)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: getwd: %v\n", err)
		os.Exit(1)
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	repoName, repoPath, cmdBody, ok := lookup(m, cwd, name)
	if !ok {
		if repoName == "" {
			fmt.Fprintf(os.Stderr, "run: no devm repo in current directory (cwd: %s)\n", cwd)
		} else {
			fmt.Fprintf(os.Stderr, "run: no command %q in repo %q\n", name, repoName)
		}
		os.Exit(1)
	}

	cmd := exec.Command("bash", "-c", cmdBody)
	cmd.Dir = repoPath
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "run: exec bash: %v\n", err)
		os.Exit(1)
	}
}

// lookup finds the repo whose guestPath is a prefix of cwd (deepest wins,
// walking up from cwd). Returns (repoName, repoPath, cmdBody, ok).
// ok=false and repoName="" ⇒ cwd is outside any repo.
// ok=false and repoName!="" ⇒ cwd is inside repoName but the command is
// undefined.
func lookup(m manifest, cwd, name string) (string, string, string, bool) {
	cleaned := filepath.Clean(cwd)
	for dir := cleaned; ; dir = filepath.Dir(dir) {
		for repoName, repo := range m.Repos {
			guestPath := repo.GuestPath
			if resolved, err := filepath.EvalSymlinks(guestPath); err == nil {
				guestPath = resolved
			}
			if filepath.Clean(guestPath) == dir {
				cmd, ok := repo.Commands[name]
				if !ok {
					return repoName, "", "", false
				}
				return repoName, repo.GuestPath, cmd.Exec, true
			}
		}
		if dir == "/" || dir == "." {
			return "", "", "", false
		}
	}
}
