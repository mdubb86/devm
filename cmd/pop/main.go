// pop is the guest-side binary that ships a "show this file on the
// Mac" request over to the daemon. It's installed at /opt/devm/bin/pop
// via the provisioning bundle. The daemon (internal/serviceapi/pop.go)
// resolves the path (cwd-then-project-root) and hands the resolved
// Mac path to macOS `open`.
//
// Usage:
//
//	pop <path> [-- <open-args>...]
//
// Reaches the daemon over softnet: guest TCP 192.168.127.1:81 is
// forwarded (softnet ForwardTargets.Pop, per project) to the daemon's
// per-project pop HTTP listener.
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

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pop <path> [-- <open-args>...]")
		os.Exit(2)
	}

	// Split "<path> [-- <open-args>...]" — everything before "--" is
	// the path (should be one arg), everything after is forwarded to
	// open.
	var pathArg string
	var openArgs []string
	if idx := indexOf(args, "--"); idx >= 0 {
		if idx == 0 {
			fmt.Fprintln(os.Stderr, "pop: missing path before --")
			os.Exit(2)
		}
		pathArg = args[0]
		openArgs = args[idx+1:]
	} else {
		pathArg = args[0]
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pop: cannot resolve cwd: %v\n", err)
		os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "pop: marshal request: %v\n", err)
		os.Exit(1)
	}

	resp, err := http.Post(popEndpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "pop: could not reach devm daemon on 192.168.127.1:81 — is the VM properly started?\n%v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(os.Stderr, resp.Body)
		os.Exit(1)
	}
	io.Copy(os.Stdout, resp.Body)
}

func indexOf(ss []string, s string) int {
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
