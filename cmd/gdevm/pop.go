package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const popEndpoint = "http://192.168.127.1:81/pop"

// popMain implements `gdevm pop`. Returns the process exit code.
//
// The daemon (internal/serviceapi/pop.go) either resolves a filesystem
// path (cwd-then-project-root) to its Mac-side mirror and hands it to
// macOS `open`, or — when the arg is an http:// / https:// URL —
// passes the URL straight to `open`.
//
// Reaches the daemon over softnet: guest TCP 192.168.127.1:81 is
// forwarded (softnet ForwardTargets.Pop, per project) to the daemon's
// per-project pop HTTP listener.
func popMain(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gdevm pop <path-or-url> [-- <open-args>...]")
		return 2
	}

	// Split "<path> [-- <open-args>...]" — everything before "--" is
	// the path (should be one arg), everything after is forwarded to
	// open.
	var pathArg string
	var openArgs []string
	if idx := indexOfString(args, "--"); idx >= 0 {
		if idx == 0 {
			fmt.Fprintln(os.Stderr, "gdevm pop: missing path before --")
			return 2
		}
		pathArg = args[0]
		openArgs = args[idx+1:]
	} else {
		pathArg = args[0]
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm pop: cannot resolve cwd: %v\n", err)
		return 1
	}

	bodyMap := map[string]any{
		"arg":       pathArg,
		"cwd":       cwd,
		"open_args": openArgs,
	}
	if !strings.HasPrefix(pathArg, "http://") && !strings.HasPrefix(pathArg, "https://") {
		resolved, isDir, ok := resolveGuestPath(pathArg, cwd)
		if ok {
			bodyMap["resolved_path"] = resolved
			bodyMap["is_dir"] = isDir
		}
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm pop: marshal request: %v\n", err)
		return 1
	}

	resp, err := http.Post(popEndpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm pop: could not reach devm daemon on 192.168.127.1:81 — is the VM properly started?\n%v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(os.Stderr, resp.Body)
		return 1
	}
	io.Copy(os.Stdout, resp.Body)
	return 0
}

func indexOfString(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// resolveGuestPath stat's the arg, following symlinks, and reports its
// canonical absolute path and whether it's a directory. Returns ok=false
// on stat error, on non-regular/non-directory types (socket, device,
// pipe — mutagen won't handle these), or when EvalSymlinks fails.
func resolveGuestPath(arg, cwd string) (canonical string, isDir bool, ok bool) {
	var candidate string
	if filepath.IsAbs(arg) {
		candidate = arg
	} else {
		candidate = filepath.Join(cwd, arg)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", false, false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false, false
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", false, false
	}
	m := fi.Mode()
	if m.IsRegular() {
		return resolved, false, true
	}
	if m.IsDir() {
		return resolved, true, true
	}
	return "", false, false
}
