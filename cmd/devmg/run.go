package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const defaultManifest = "/opt/devm/commands.json"

type manifest struct {
	Repos map[string]struct {
		GuestPath string   `json:"guestPath"`
		Commands  []string `json:"commands"`
	} `json:"repos"`
}

// runMain implements `devmg run`. Reads /opt/devm/commands.json, walks
// up from $PWD to find its containing repo, verifies the requested
// name is registered for that repo, then sources devm.sh (+devm.me.sh)
// and syscall.Exec's bash to invoke the named function from the repo's
// guest path. Never returns on success — bash replaces this process.
func runMain(args []string) int {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: devmg run <command>")
		return 2
	}
	name := args[0]

	manifestPath := os.Getenv("DEVM_COMMANDS_MANIFEST")
	if manifestPath == "" {
		manifestPath = defaultManifest
	}
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "devmg run: cannot read manifest %s: %v\n", manifestPath, err)
		return 1
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		fmt.Fprintf(os.Stderr, "devmg run: parse manifest: %v\n", err)
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "devmg run: getwd: %v\n", err)
		return 1
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	repoName, guestPath, ok := findRepo(m, cwd)
	if !ok {
		fmt.Fprintln(os.Stderr, "devmg run: not inside a registered repo")
		return 1
	}
	if !registered(m.Repos[repoName].Commands, name) {
		fmt.Fprintf(os.Stderr, "devmg run: command %s not registered in repo %s\n", name, repoName)
		return 1
	}

	scriptBody := fmt.Sprintf(`set -eo pipefail
source /home/devm/devm.sh
[ -f /home/devm/devm.me.sh ] && source /home/devm/devm.me.sh
cd "%s"
%s`, guestPath, name)

	if err := syscall.Exec("/bin/bash", []string{"bash", "-c", scriptBody}, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "devmg run: exec bash: %v\n", err)
		return 1
	}
	return 0 // unreachable
}

// findRepo finds the repo whose guestPath is a prefix of cwd (walking up
// from cwd). Returns (repoName, guestPath, ok); ok=false means cwd is
// outside every registered repo.
func findRepo(m manifest, cwd string) (string, string, bool) {
	cleaned := filepath.Clean(cwd)
	for dir := cleaned; ; dir = filepath.Dir(dir) {
		for repoName, repo := range m.Repos {
			guestPath := repo.GuestPath
			if resolved, err := filepath.EvalSymlinks(guestPath); err == nil {
				guestPath = resolved
			}
			if filepath.Clean(guestPath) == dir {
				return repoName, repo.GuestPath, true
			}
		}
		if dir == "/" || dir == "." {
			return "", "", false
		}
	}
}

// registered reports whether name appears in commands.
func registered(commands []string, name string) bool {
	for _, c := range commands {
		if c == name {
			return true
		}
	}
	return false
}
