// devm-docker-shim intercepts docker CLI invocations inside the devm
// sandbox and appends `--secret id=devm-ca,src=/etc/ssl/certs/devm.crt`
// on `docker build` / `docker buildx build`. On every other subcommand
// it exec-forwards the argv unchanged.
//
// The shim is installed at /usr/local/bin/docker so it shadows the
// real docker at /usr/bin/docker via the standard PATH order
// (/usr/local/bin first). At exec time we strip our own directory
// from PATH and look up "docker" in what's left — no hardcoded path,
// portable across distro changes.
//
// Why the daemon+shim split: BuildKit's build sandbox goes through
// iron-proxy's MITM path (unlike plain `docker run`, which uses
// SNI-passthrough on the bridge). Users write one Dockerfile RUN
// block that mounts a `type=secret,id=devm-ca` and installs it —
// this shim guarantees the flag is present so the mount is populated
// inside the sandbox, and stays a portable no-op on Mac/CI where the
// user runs plain docker without the shim (required=false on the
// mount + a shell test that skips the update when the file is empty).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// caPath is where devm's CA lives in the guest. Provisioner writes
// it here directly (see internal/provision/provision.go's
// installCARootScript), then runs update-ca-certificates so the
// content is merged into /etc/ssl/certs/ca-certificates.crt. This
// source file is untouched by update-ca-certificates and stays
// stable across devm versions and reprovisions. Do NOT switch to
// /etc/ssl/certs/devm.crt — update-ca-certificates creates
// hash-named symlinks (openssl-style) there, not the friendly name.
const caPath = "/usr/local/share/ca-certificates/devm.crt"

// secretID is the buildkit secret id the injected flag exposes.
// Users reference this from Dockerfile's RUN --mount=type=secret,id=.
const secretID = "devm-ca"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "devm-docker-shim: %v\n", err)
		os.Exit(1)
	}
	// unreachable: run() always either exec's or returns an error.
}

func run(argv []string) error {
	if shouldInjectSecret(argv) {
		argv = append(argv, "--secret", "id="+secretID+",src="+caPath)
	}
	return execDocker(argv)
}

// shouldInjectSecret reports whether argv is a `docker build` or
// `docker buildx build` invocation — the two forms where BuildKit
// runs RUN steps in the sandbox that MITMs through iron-proxy.
//
// argv here is os.Args[1:], so argv[0] is the first arg after
// `docker`. We skip global docker CLI flags (things like `--context`,
// `--host`, `-l`, etc.) to find the first positional token; that's
// the subcommand. For `buildx`, we then peek at the next positional
// to catch the `build` subsubcommand.
func shouldInjectSecret(argv []string) bool {
	first, rest, ok := firstPositional(argv)
	if !ok {
		return false
	}
	switch first {
	case "build":
		return true
	case "buildx":
		second, _, ok := firstPositional(rest)
		return ok && second == "build"
	}
	return false
}

// firstPositional returns the first non-flag token in argv, the slice
// of everything after it, and whether one was found. Handles both
// "--flag value" and "--flag=value" forms; for "--flag value" we skip
// the value when the flag is known to take one.
//
// Docker's global flags that take a value: --config, --context, -c,
// --host, -H, --log-level, -l, --tls-verify-tunnel-hostname (rare
// enterprise ones excluded). Anything else is treated as boolean —
// worst case we treat a value token as a subcommand and miss the
// injection; the build then goes through without --secret, users
// hit the same "no CA in sandbox" error and know to reach for
// documentation. Better to miss an injection than inject at the wrong
// spot in the argv.
func firstPositional(argv []string) (string, []string, bool) {
	valuedFlags := map[string]bool{
		"--config":    true,
		"--context":   true,
		"-c":          true,
		"--host":      true,
		"-H":          true,
		"--log-level": true,
		"-l":          true,
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "-") {
			return a, argv[i+1:], true
		}
		if strings.Contains(a, "=") {
			continue // --flag=value: one token, already covered.
		}
		if valuedFlags[a] && i+1 < len(argv) {
			i++ // skip the value token.
		}
	}
	return "", nil, false
}

// execDocker resolves the real docker binary (anything on PATH after
// our own directory is removed) and syscall.Exec's it with argv
// preserved as-is. syscall.Exec replaces the current process — no
// process left behind, no exit code re-plumbing needed.
func execDocker(argv []string) error {
	real, err := resolveRealDocker()
	if err != nil {
		return err
	}
	full := append([]string{real}, argv...)
	if err := syscall.Exec(real, full, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", real, err)
	}
	return nil // unreachable — syscall.Exec on success does not return.
}

// resolveRealDocker finds the docker binary that the shim is
// shadowing. os.Args[0]'s directory is our own install dir
// (/usr/local/bin under normal install); strip it from PATH and
// exec.LookPath("docker") in what remains.
//
// Robust to base image changes that move docker between /usr/bin,
// /usr/local/bin (unlikely — that'd be a self-reference), or an
// alternative location: as long as the real docker is on PATH, we
// find it. Falls back to a clear error if the search yields nothing
// or resolves back to our own binary.
func resolveRealDocker() (string, error) {
	selfExe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve self: %w", err)
	}
	selfExe, _ = filepath.EvalSymlinks(selfExe)
	selfDir := filepath.Dir(selfExe)

	paths := filepath.SplitList(os.Getenv("PATH"))
	kept := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" || p == selfDir {
			continue
		}
		kept = append(kept, p)
	}
	restore := os.Getenv("PATH")
	if err := os.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator))); err != nil {
		return "", fmt.Errorf("rewrite PATH: %w", err)
	}
	defer func() { _ = os.Setenv("PATH", restore) }()

	real, err := exec.LookPath("docker")
	if err != nil {
		return "", fmt.Errorf("locate real docker (PATH minus %s): %w", selfDir, err)
	}
	realResolved, _ := filepath.EvalSymlinks(real)
	if realResolved == selfExe {
		return "", fmt.Errorf("PATH lookup resolved back to the shim (%s) — refusing to exec-loop", real)
	}
	return real, nil
}
