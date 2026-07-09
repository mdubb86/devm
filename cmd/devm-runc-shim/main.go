// devm-runc-shim intercepts OCI runtime invocations from dockerd
// and mediates the container's OCI spec so that iron-proxy's CA is
// trusted transparently. On create/run, appends one bind-mount of
// /etc/ssl/certs/ca-certificates.crt into the container rootfs, then
// exec's real /usr/bin/runc with the caller's args unchanged. All
// other subcommands pass through untouched.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	realRuncPath = "/usr/bin/runc"
	caBundlePath = "/etc/ssl/certs/ca-certificates.crt"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "devm-runc-shim: %v\n", err)
		os.Exit(1)
	}
	// unreachable: run() always either exec's or returns error
}

func run(argv []string) error {
	sub := subcmd(argv)
	if sub != "create" && sub != "run" {
		return execRunc(argv)
	}
	bundle := bundleFromArgs(argv)
	if bundle == "" {
		// No bundle to modify. Pass through — runc will complain on
		// its own if it needs one.
		return execRunc(argv)
	}
	if err := injectCA(bundle); err != nil {
		return fmt.Errorf("inject CA into %s: %w", bundle, err)
	}
	return execRunc(argv)
}

// subcmd returns the first non-global-flag arg, or "" if none.
// Global flags recognised: --root, --log, --log-format, --systemd-cgroup,
// --rootless, --debug, --criu. Values for --root, --log, --log-format,
// --criu, --rootless (when it takes a value) are single tokens.
func subcmd(argv []string) string {
	valuedFlags := map[string]bool{
		"--root": true, "--log": true, "--log-format": true, "--criu": true,
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "-") {
			return a
		}
		// Handle --flag=value form: no extra skip needed.
		if strings.Contains(a, "=") {
			continue
		}
		// Handle --flag value form for valuedFlags.
		base := a
		if valuedFlags[base] && i+1 < len(argv) {
			i++ // skip value
		}
	}
	return ""
}

// bundleFromArgs returns the value of --bundle, or "" if absent.
// Handles both "--bundle X" and "--bundle=X" forms.
func bundleFromArgs(argv []string) string {
	for i, a := range argv {
		if a == "--bundle" && i+1 < len(argv) {
			return argv[i+1]
		}
		if strings.HasPrefix(a, "--bundle=") {
			return strings.TrimPrefix(a, "--bundle=")
		}
	}
	return ""
}

// injectCA reads config.json in the bundle, appends the CA bind-mount
// (if the destination file exists in the container rootfs and the
// mount isn't already present), and atomically writes back.
func injectCA(bundle string) error {
	cfgPath := filepath.Join(bundle, "config.json")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var spec map[string]any
	if err := json.Unmarshal(body, &spec); err != nil {
		return fmt.Errorf("parse config.json: %w", err)
	}

	// Rootfs probe: if destination file doesn't exist in the container
	// rootfs, skip. Distroless / scratch images stay working.
	rootPath := ""
	if root, ok := spec["root"].(map[string]any); ok {
		if p, ok := root["path"].(string); ok {
			rootPath = p
		}
	}
	if rootPath == "" {
		return errors.New("spec.root.path missing")
	}
	// runc allows rootPath to be relative to the bundle.
	if !filepath.IsAbs(rootPath) {
		rootPath = filepath.Join(bundle, rootPath)
	}
	destInRootfs := filepath.Join(rootPath, caBundlePath)
	if _, err := os.Stat(destInRootfs); err != nil {
		// File doesn't exist in the rootfs (distroless, or non-standard
		// layout). Skip the mount — container starts without CA trust.
		return nil
	}

	// Idempotent: if our mount is already there (e.g. re-create),
	// skip append.
	mounts, _ := spec["mounts"].([]any)
	for _, m := range mounts {
		if entry, ok := m.(map[string]any); ok {
			if entry["destination"] == caBundlePath {
				return nil
			}
		}
	}

	spec["mounts"] = append(mounts, map[string]any{
		"source":      caBundlePath,
		"destination": caBundlePath,
		"options":     []any{"bind", "ro"},
	})

	newBody, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("re-marshal spec: %w", err)
	}

	// Atomic write: temp file in same dir + rename.
	tmp, err := os.CreateTemp(bundle, "config.json.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(newBody); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, cfgPath)
}

// execRunc replaces this process with real runc, forwarding argv.
// Returns only on failure to exec.
func execRunc(argv []string) error {
	full := append([]string{realRuncPath}, argv...)
	return syscall.Exec(realRuncPath, full, os.Environ())
}
