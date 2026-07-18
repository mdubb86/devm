// Package tart wraps the `tart` CLI (github.com/cirruslabs/tart). The
// devm daemon supervises Tart VMs through this thin shim — we don't
// link against Tart's source (it's a Swift binary), so we shell out
// to its CLI exclusively.
package tart

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tart wraps the `tart` binary on PATH.
type Tart struct {
	Path string // tart binary path; defaults to "tart" (resolved via PATH)
}

// New constructs a Tart wrapper with the default binary path.
func New() *Tart { return &Tart{Path: "tart"} }

// RunOpts controls `tart run`.
type RunOpts struct {
	NoGraphics bool       // adds --no-graphics
	DirMounts  []DirMount // each becomes a --dir=<spec> arg
	NetSoftnet bool       // adds --net-softnet
}

// DirMount is a virtio-fs shared directory.
type DirMount struct {
	HostPath string
	Tag      string // virtio-fs mount tag; empty uses Tart default ("com.apple.virtio-fs.automount")
	ReadOnly bool
	Name     string // friendly name shown as subdirectory under the mount tag
}

// ExecResult is the captured output + exit code from `tart exec`.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// VM is one entry from `tart list`.
type VM struct {
	Name    string
	Running bool
}

// Pull fetches an image into Tart's local cache.
func (t *Tart) Pull(ctx context.Context, image string) error {
	return t.run(ctx, "pull", image).err
}

// Clone copies an image to a new local VM.
func (t *Tart) Clone(ctx context.Context, src, dst string) error {
	return t.run(ctx, "clone", src, dst).err
}

// SetDiskSize grows the VM's virtual disk to gib gigabytes via
// `tart set <name> --disk-size <gib>`. The VM must be stopped. tart
// resize is GROW-ONLY: a gib smaller than the current disk errors; a
// gib equal to the current size is a no-op that exits 0. Growing the
// raw disk does NOT grow the guest filesystem — the caller must run
// growpart + resize2fs inside the guest afterward.
func (t *Tart) SetDiskSize(ctx context.Context, name string, gib int) error {
	return t.run(ctx, "set", name, "--disk-size", strconv.Itoa(gib)).err
}

// Run prepares an unstarted exec.Cmd for `tart run`. The caller (a
// supervisor) decides how to start, detach, log, and reap it. We DON'T
// set SysProcAttr.Setsid here — that's the supervisor's job (different
// concerns per call site; the supervisor knows what daemon-survivability
// posture is required).
func (t *Tart) Run(ctx context.Context, name string, opts RunOpts) (*exec.Cmd, error) {
	args := []string{"run"}
	if opts.NoGraphics {
		args = append(args, "--no-graphics")
	}
	for _, m := range opts.DirMounts {
		args = append(args, t.formatDirArg(m))
	}
	if opts.NetSoftnet {
		args = append(args, "--net-softnet")
	}
	args = append(args, name)
	return exec.CommandContext(ctx, t.Path, args...), nil
}

// Stop signals the running VM to shut down gracefully.
func (t *Tart) Stop(ctx context.Context, name string) error {
	return t.run(ctx, "stop", name).err
}

// Delete removes a (stopped) VM's disk image.
func (t *Tart) Delete(ctx context.Context, name string) error {
	return t.run(ctx, "delete", name).err
}

// IP returns the VM's current IPv4 address (typically a 192.168.64.x
// for --net-shared). Returns an error if the VM isn't running or its
// network isn't up yet — caller can retry with backoff.
func (t *Tart) IP(ctx context.Context, name string) (string, error) {
	r := t.run(ctx, "ip", name)
	if r.err != nil {
		return "", r.err
	}
	return strings.TrimSpace(r.stdout), nil
}

// Exec runs a command inside the VM via `tart exec`. Captures stdout,
// stderr, and exit code. Does NOT retry — callers that want defense
// against transient tart-guest-agent transport failures should use
// ExecWithRetry.
func (t *Tart) Exec(ctx context.Context, name string, argv []string) ExecResult {
	args := append([]string{"exec", name}, argv...)
	cmd := exec.CommandContext(ctx, t.Path, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
	}
	return ExecResult{Stdout: so.String(), Stderr: se.String(), ExitCode: code}
}

// ExecStdin runs a command inside the VM via `tart exec -i`, piping
// the given reader as the command's stdin. Same capture + exit-code
// semantics as Exec. Use for delivering large binary payloads that
// would overflow an argv slot.
func (t *Tart) ExecStdin(ctx context.Context, name string, stdin io.Reader, argv []string) ExecResult {
	args := append([]string{"exec", "-i", name}, argv...)
	cmd := exec.CommandContext(ctx, t.Path, args...)
	cmd.Stdin = stdin
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
	}
	return ExecResult{Stdout: so.String(), Stderr: se.String(), ExitCode: code}
}

// ExecStream runs `tart exec -i <name> <argv>`, piping stdin in and streaming
// stdout/stderr line-by-line to onLine ("stdout"/"stderr") as they arrive.
// Returns the guest command's exit code. Unlike Exec, output is NOT buffered
// into ExecResult — the caller drives progress live. onLine may be nil.
//
// onLine is invoked from two concurrent goroutines (one per stream) with no
// serialization between them — if it touches shared state, the caller must
// synchronize inside onLine.
func (t *Tart) ExecStream(ctx context.Context, name string, stdin io.Reader,
	argv []string, onLine func(stream, line string)) (int, error) {
	args := append([]string{"exec", "-i", name}, argv...)
	cmd := exec.CommandContext(ctx, t.Path, args...)
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}

	var wg sync.WaitGroup
	scan := func(r io.Reader, stream string) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if onLine != nil {
				onLine(stream, sc.Text())
			}
		}
	}
	wg.Add(2)
	go scan(stdout, "stdout")
	go scan(stderr, "stderr")
	wg.Wait()

	err = cmd.Wait()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// ExecWithRetry runs Exec, and if the result is the specific
// "Transport became inactive" (gRPC unavailable (14)) tart-guest-agent
// flake, sleeps 2s and retries once. Any other non-zero exit is
// returned as-is — a legitimate command failure must reach the caller.
//
// Use this instead of Exec when the failure of a step would kill a
// longer-running operation (e.g. provisioning, snapshot IO, apply-live
// template installs). Do NOT use this in polling loops that already
// have their own retry logic (e.g. waitVMReady) — you'd double-retry.
func (t *Tart) ExecWithRetry(ctx context.Context, name string, argv []string) ExecResult {
	r := t.Exec(ctx, name, argv)
	if IsTransportInactive(r) {
		select {
		case <-ctx.Done():
			return r
		case <-time.After(2 * time.Second):
		}
		r = t.Exec(ctx, name, argv)
	}
	return r
}

// IsTransportInactive detects tart-guest-agent's gRPC transport flakes
// — HTTP/2 GOAWAY, connection-drop, or header-send races on the
// guest-side RPC channel. The gRPC codes are stable across versions:
//
//	"unavailable (14)"   — UNAVAILABLE
//	"internal error (13)" — INTERNAL
//
// Manifests as a non-zero exit with one of the marker strings in stderr.
// Keep in sync with e2e/helpers/tart.py's `_TRANSPORT_FLAKE_MARKERS`.
func IsTransportInactive(r ExecResult) bool {
	if r.ExitCode == 0 {
		return false
	}
	markers := []string{
		"Transport became inactive",
		"unavailable (14)",
		"SendHeader called multiple times",
		"internal error (13)",
	}
	for _, m := range markers {
		if strings.Contains(r.Stderr, m) {
			return true
		}
	}
	return false
}

// List returns all VMs Tart knows about (running or stopped).
func (t *Tart) List(ctx context.Context) ([]VM, error) {
	r := t.run(ctx, "list", "--format", "json")
	if r.err != nil {
		return nil, r.err
	}
	return parseListJSON(r.stdout)
}

func (t *Tart) formatDirArg(m DirMount) string {
	// Tart's --dir syntax: --dir=name:hostpath[:ro][:tag=customtag]
	spec := m.HostPath
	if m.ReadOnly {
		spec += ":ro"
	}
	if m.Tag != "" {
		spec += ":tag=" + m.Tag
	}
	if m.Name != "" {
		spec = m.Name + ":" + spec
	}
	return "--dir=" + spec
}

type runResult struct {
	stdout string
	stderr string
	err    error
}

func (t *Tart) run(ctx context.Context, args ...string) runResult {
	cmd := exec.CommandContext(ctx, t.Path, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	if err != nil {
		err = fmt.Errorf("tart %s: %w (stderr: %s)",
			strings.Join(args, " "), err, strings.TrimSpace(se.String()))
	}
	return runResult{stdout: so.String(), stderr: se.String(), err: err}
}

// parseListJSON pulls VM names + running state out of `tart list --format json`.
// We do a permissive decode (interface{} → map) because Tart's exact field
// names may shift across versions, and we only need name + state.
func parseListJSON(raw string) ([]VM, error) {
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("tart list: parse json: %w (raw: %s)", err, raw)
	}
	out := make([]VM, 0, len(entries))
	for _, e := range entries {
		name, _ := e["Name"].(string)
		if name == "" {
			name, _ = e["name"].(string)
		}
		state, _ := e["State"].(string)
		if state == "" {
			state, _ = e["state"].(string)
		}
		out = append(out, VM{Name: name, Running: state == "running"})
	}
	return out, nil
}
