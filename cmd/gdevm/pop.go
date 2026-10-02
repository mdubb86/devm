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
	"time"
)

// popEndpoint is one route on the Mac-side daemon's per-project
// guest-API HTTP listener. Softnet forwards guest TCP 192.168.127.1:81
// to the per-project listener on the Mac (ForwardTargets.GuestAPI),
// which serves /pop alongside /propose, /passthrough, /refresh-bundle,
// and /recipes/* under one mux.
const popEndpoint = "http://192.168.127.1:81/pop"

// popBody is the on-wire shape POST /pop expects. Mirrors
// internal/serviceapi.PopRequest.
type popBody struct {
	GuestPath string   `json:"guest_path"`
	Native    bool     `json:"native,omitempty"`
	OpenArgs  []string `json:"open_args,omitempty"`
}

const popUsage = `usage: gdevm pop [--native] <path> [-- open-args...]

Open <path> on the Mac. By default <path> opens in the Mac's browser
via the project's bundled filestash service — any file under /, no
mirror lookup needed. With --native, the daemon resolves <path>
through the project's mirror table and opens the live Mac mirror
file (so edits sync back the usual way); if <path> is out of mirror,
the daemon ` + "`tart exec cat`" + `'s it to a scratch dir on the Mac and opens
the scratch copy.

<path> can be absolute (/home/devm/repo/foo.html) or relative to the
current directory. The guest resolves the path before posting it to
the daemon.

Flags:
  --native            Use the macOS default app via ` + "`open <file>`" + `
                      instead of filestash in a browser.
  -h, --help          Print this help.
  -- open-args...     Everything after a bare ` + "`--`" + ` is passed through
                      to ` + "`open`" + ` on the Mac (e.g. ` + "`-- -a 'Google Chrome'`" + `).
`

// popMain implements `gdevm pop`. Returns the process exit code.
func popMain(args []string) int {
	native := false
	var path string
	var openArgs []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(popUsage)
			return 0
		case "--native":
			native = true
		case "--":
			openArgs = append(openArgs, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "gdevm pop: unknown flag %q\n%s", args[i], popUsage)
				return 2
			}
			if path != "" {
				fmt.Fprintf(os.Stderr, "gdevm pop: unexpected extra arg %q (use -- to pass open args)\n%s", args[i], popUsage)
				return 2
			}
			path = args[i]
		}
	}

	if path == "" {
		fmt.Fprint(os.Stderr, "gdevm pop: <path> is required\n"+popUsage)
		return 2
	}

	absPath, err := absolutizeGuestPath(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm pop: %v\n", err)
		return 1
	}

	return doPopPost(popEndpoint, absPath, native, openArgs)
}

// absolutizeGuestPath turns path into an absolute guest-side path.
// Absolute paths pass through; relative paths resolve against the
// current directory. filepath.Clean normalises `..` segments.
func absolutizeGuestPath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cwd: %w", err)
	}
	return filepath.Clean(filepath.Join(cwd, path)), nil
}

// doPopPost sends the pop request and translates the daemon's response
// into a process exit code. The daemon returns 200 with the opened
// target on stdout; non-2xx surfaces as stderr with a non-zero exit.
func doPopPost(endpoint, guestPath string, native bool, openArgs []string) int {
	body, _ := json.Marshal(popBody{
		GuestPath: guestPath,
		Native:    native,
		OpenArgs:  openArgs,
	})
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm pop: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm pop: cannot reach devm daemon on 192.168.127.1:81 — is the VM properly started?\n%v\n", err)
		return 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		fmt.Print(string(respBody))
		return 0
	}
	fmt.Fprintf(os.Stderr, "gdevm pop: daemon returned %d: %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
	if resp.StatusCode == http.StatusNotFound {
		return 2
	}
	return 1
}
