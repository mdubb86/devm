// devm-docker-shim intercepts docker CLI invocations and routes bare
// `docker build` through the devm-managed buildx builder ("devm").
// That builder runs a devm-controlled buildkitd whose OCI worker is
// devm-runc-shim, so every RUN-step container gets iron-proxy CA
// trust plus caenv.Vars env-var injection transparently — same shim
// path as docker run/create/exec.
//
// On every other subcommand (including `buildx build` where the user
// passed their own --builder) the shim exec-forwards argv unchanged.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// builderName is the fixed buildx builder devm installs and routes
// builds through. Registered by internal/docker/install.go.
const builderName = "devm"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "devm-docker-shim: %v\n", err)
		os.Exit(1)
	}
	// unreachable: run() either exec's or returns an error.
}

func run(argv []string) error {
	rewritten, err := transformArgv(argv)
	if err != nil {
		return err
	}
	return execDocker(rewritten)
}

// transformArgv rewrites bare `docker build …` → `docker buildx build
// --builder devm …`, and injects `--builder devm` into `docker buildx
// build …` when the user hasn't set one. Every other subcommand
// (including buildx subcommands other than build, and buildx build
// with a user-set --builder) passes through unchanged.
//
// Returns an error only when the rewrite would land on a builder that
// isn't currently healthy — verifyBuilderHealthy checks `docker buildx
// inspect devm` and fails loud with a "run devm reconcile" message.
// No auto-repair.
func transformArgv(argv []string) ([]string, error) {
	first, rest, ok := firstPositional(argv)
	if !ok {
		return argv, nil
	}
	switch first {
	case "build":
		if err := verifyBuilderHealthy(); err != nil {
			return nil, err
		}
		return rewriteBuild(argv, rest), nil
	case "buildx":
		second, _, ok := firstPositional(rest)
		if !ok || second != "build" {
			return argv, nil
		}
		if userSetBuilder(argv) {
			return argv, nil
		}
		if err := verifyBuilderHealthy(); err != nil {
			return nil, err
		}
		return injectBuilder(argv), nil
	default:
		return argv, nil
	}
}

// rewriteBuild replaces the `build` subcommand token with the sequence
// `buildx build --builder devm`, leaving preceding global flags and
// following args intact. Also auto-appends `--load` when the user
// hasn't specified an output form of their own — see loadNeeded for
// why bare `docker build` requires it under the devm builder.
func rewriteBuild(argv, rest []string) []string {
	// argv[insertAt] == "build". Everything after "build" is `rest`.
	insertAt := len(argv) - len(rest) - 1
	out := make([]string, 0, len(argv)+5)
	out = append(out, argv[:insertAt]...)
	out = append(out, "buildx", "build", "--builder", builderName)
	if loadNeeded(rest) {
		out = append(out, "--load")
	}
	out = append(out, rest...)
	return out
}

// loadNeeded reports whether we should auto-inject `--load` into a
// rewritten `docker build`. Bare `docker build` (legacy CLI) always
// puts the built image in the local image store — that's the whole
// point of `docker build && docker run <tag>`. Under buildx's `remote`
// driver (which the devm builder uses), the default output is nothing
// but the build cache: the tag never lands in the local image store
// and any subsequent `docker run <tag>` fails "image not found". So
// preserve the legacy semantic by auto-adding --load — UNLESS the
// user has already told buildx where to send the result (--load,
// --push, -o / --output). Second-guessing an explicit --push or
// --output would conflict with the user's intent or with docker
// itself (--push + --load isn't valid; --output type=tar and --load
// point in different directions).
func loadNeeded(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--load", "--push", "-o", "--output":
			return false
		}
		if strings.HasPrefix(a, "--output=") || strings.HasPrefix(a, "-o=") {
			return false
		}
		// Skip a valued flag's value so we don't misread e.g. `-t -o`
		// (tag literally "-o") as an output flag.
		if buildxBuildValuedFlags[a] && i+1 < len(args) {
			i++
		}
	}
	return true
}

// injectBuilder finds the `build` token that follows `buildx` and
// inserts `--builder devm` immediately after it. The user's argv is
// otherwise unchanged.
func injectBuilder(argv []string) []string {
	for i, a := range argv {
		if a != "buildx" {
			continue
		}
		for j := i + 1; j < len(argv); j++ {
			if argv[j] == "build" && !strings.HasPrefix(argv[j], "-") {
				out := make([]string, 0, len(argv)+2)
				out = append(out, argv[:j+1]...)
				out = append(out, "--builder", builderName)
				out = append(out, argv[j+1:]...)
				return out
			}
		}
	}
	return argv // unreachable when called after the buildx/build match
}

// buildxBuildValuedFlags names docker global + `docker buildx build`
// flags that take a value in the next argv token, for the subset whose
// value could plausibly be the literal string "--builder" (a filename
// via -f, a tag via -t, a docker context path). Any flag NOT in this
// map is treated as boolean/self-contained-value for arity purposes.
// Newer buildx versions may add more valued flags; the worst case of
// a missing entry is the same class of false-positive this map exists
// to prevent — a duplicate --builder gets injected and docker errors
// loudly, not silently.
var buildxBuildValuedFlags = map[string]bool{
	// Docker global flags (can precede `buildx`).
	"--config": true, "--context": true, "-c": true,
	"--host": true, "-H": true,
	"--log-level": true, "-l": true,
	// `docker buildx build` valued flags whose value could be "--builder".
	"-f": true, "--file": true,
	"-t": true, "--tag": true,
}

// userSetBuilder reports whether argv contains an explicit --builder
// or --builder= flag at flag position (not swallowed as a value by
// a preceding valued flag like -f or -t). If yes, we respect the
// user's choice and skip our injection entirely.
func userSetBuilder(argv []string) bool {
	i := 0
	for i < len(argv) {
		a := argv[i]
		if a == "--builder" && i+1 < len(argv) {
			return true
		}
		if strings.HasPrefix(a, "--builder=") {
			return true
		}
		if buildxBuildValuedFlags[a] && i+1 < len(argv) {
			i += 2
			continue
		}
		i++
	}
	return false
}

// verifyBuilderHealthyFn is a test seam. Production shells out to
// `docker buildx inspect devm` and checks exit status.
var verifyBuilderHealthyFn = realVerifyBuilderHealthy

func verifyBuilderHealthy() error { return verifyBuilderHealthyFn() }

func realVerifyBuilderHealthy() error {
	real, err := resolveRealDocker()
	if err != nil {
		return err
	}
	cmd := exec.Command(real, "buildx", "inspect", builderName)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"buildx builder %q not found or unhealthy.\nRun 'devm reconcile' to restore it",
			builderName,
		)
	}
	return nil
}

// firstPositional returns the first non-flag token in argv, the slice
// of everything after it, and whether one was found. Handles both
// "--flag value" and "--flag=value" forms; for "--flag value" we skip
// the value when the flag is known to take one.
//
// Docker global flags that take a value: --config, --context/-c,
// --host/-H, --log-level/-l. Anything else is treated as boolean —
// worst case we treat a value token as a subcommand; the argv passes
// through unchanged and docker itself errors.
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
			continue
		}
		if valuedFlags[a] && i+1 < len(argv) {
			i++
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
	return nil
}

// resolveRealDocker finds the docker binary that the shim is
// shadowing. os.Args[0]'s directory is our own install dir
// (/usr/local/bin under normal install); strip it from PATH and
// exec.LookPath("docker") in what remains.
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
