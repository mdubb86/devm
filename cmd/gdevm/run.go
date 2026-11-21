package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const defaultManifest = "/opt/devm/commands.json"

type manifest struct {
	Repos map[string]struct {
		GuestPath string   `json:"guestPath"`
		Commands  []string `json:"commands"`
	} `json:"repos"`
}

// runMain implements `gdevm run [-d|--detach] <name> [args...]`. The
// client POSTs the invocation to the gdevm serve daemon at /v1/run;
// the daemon spawns bash as its own child in a new process group,
// which makes the run's lifetime independent of the client's shell
// (critical for `nohup gdevm run <name> &` style long-running
// services and for `gdevm run -d` detached starts). `gdevm stop
// <name>` and `gdevm logs <name>` manage the run after start.
//
// For attached runs (no -d), the response body is a stream of
// framed output chunks followed by an exit-code frame; the client
// decodes, routes output to stdout, and returns the exit code. For
// detached runs, the server returns 201 immediately after starting
// the process and the client prints the started-run info and exits.
func runMain(args []string) int {
	fs := flag.NewFlagSet("gdevm run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	detach := fs.Bool("detach", false, "start the run in the background; return immediately")
	fs.BoolVar(detach, "d", false, "shorthand for --detach")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gdevm run [-d|--detach] <command> [args...]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 || rest[0] == "" {
		fs.Usage()
		return 2
	}
	name := rest[0]
	cmdArgs := rest[1:]

	// Resolve cwd client-side so the server knows which repo the
	// invocation belongs to; the server does its own findRepo as
	// authoritative validation against the commands manifest.
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm run: getwd: %v\n", err)
		return 1
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	envMap := envSliceToMap(os.Environ())
	body, err := json.Marshal(runRequest{
		Cwd:    cwd,
		Name:   name,
		Args:   cmdArgs,
		Env:    envMap,
		Detach: *detach,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm run: marshal request: %v\n", err)
		return 1
	}

	req, err := http.NewRequest(http.MethodPost, clientURL("/v1/run"), bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm run: build request: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")

	// Propagate SIGINT/SIGTERM from the user's shell by canceling
	// the HTTP request — which closes the TCP connection, which the
	// server detects via r.Context().Done() and translates into
	// stopRun. Cleaner than trying to signal remotely.
	ctx, cancel := signal.NotifyContext(req.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := clientDo(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm run: POST /v1/run: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if *detach {
		if resp.StatusCode != http.StatusCreated {
			io.Copy(os.Stderr, resp.Body)
			return 1
		}
		var ok runResponse
		if err := json.NewDecoder(resp.Body).Decode(&ok); err != nil {
			fmt.Fprintf(os.Stderr, "gdevm run: parse response: %v\n", err)
			return 1
		}
		fmt.Printf("started %s (pid %d)\n", ok.Name, ok.Pid)
		return 0
	}

	if resp.StatusCode != http.StatusOK {
		io.Copy(os.Stderr, resp.Body)
		return 1
	}

	// Attached: read frames, route to stdout, return the exit code
	// from the exit frame.
	br := bufio.NewReader(resp.Body)
	for {
		typ, payload, err := readFrame(br)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				fmt.Fprintln(os.Stderr, "gdevm run: lost connection to gdevm serve before exit frame")
				return 1
			}
			fmt.Fprintf(os.Stderr, "gdevm run: frame read: %v\n", err)
			return 1
		}
		switch typ {
		case frameOutput:
			if _, werr := os.Stdout.Write(payload); werr != nil {
				// Downstream (the user's terminal / pipe) closed.
				// Canceling the context lets the server stop the run.
				cancel()
				return 1
			}
		case frameExit:
			if len(payload) != 4 {
				fmt.Fprintf(os.Stderr, "gdevm run: malformed exit frame (len=%d)\n", len(payload))
				return 1
			}
			return int(int32(binary.BigEndian.Uint32(payload))) //nolint:gosec // exit codes are 8-bit on POSIX
		default:
			// Future-compatibility: skip unknown frame types rather
			// than fail, since the server may evolve (e.g. stderr
			// separation).
		}
	}
}

// findRepo finds the repo whose guestPath is a prefix of cwd (walking up
// from cwd). Returns (repoName, guestPath, ok); ok=false means cwd is
// outside every registered repo. Used by the server's /v1/run handler.
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

// guestDevmShPath / guestDevmMeShPath are the on-guest rc files
// sourced before the named function runs. Package vars so tests
// can swap them for fixtures on hosts without /home/devm. Prod
// values match the historical hardcoded paths.
var (
	guestDevmShPath   = "/home/devm/devm.sh"
	guestDevmMeShPath = "/home/devm/devm.me.sh"
)

// buildRunScript returns the bash -c body that sources devm.sh (+
// devm.me.sh if present), changes to the repo's guestPath, and
// invokes the named function with `"$@"` so caller args reach it.
// Pure function so arg-forwarding is unit-testable without exec.
// Shared with the server-side /v1/run handler.
func buildRunScript(guestPath, name string) string {
	return fmt.Sprintf(`set -eo pipefail
source "%s"
[ -f "%s" ] && source "%s"
cd "%s"
%s "$@"`, guestDevmShPath, guestDevmMeShPath, guestDevmMeShPath, guestPath, name)
}

// envSliceToMap splits os.Environ() entries at the first "=" into a
// map suitable for JSON marshaling. Entries without "=" are ignored.
func envSliceToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		i := strings.IndexByte(e, '=')
		if i < 0 {
			continue
		}
		out[e[:i]] = e[i+1:]
	}
	return out
}
