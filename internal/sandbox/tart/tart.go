// Package tart wraps the `tart` CLI (github.com/cirruslabs/tart). The
// devm daemon supervises Tart VMs through this thin shim — we don't
// link against Tart's source (it's a Swift binary), so we shell out
// to its CLI exclusively.
package tart

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Tart wraps the `tart` binary on PATH.
type Tart struct {
	Path string // tart binary path; defaults to "tart" (resolved via PATH)
}

// New constructs a Tart wrapper with the default binary path.
func New() *Tart { return &Tart{Path: "tart"} }

// RunOpts controls `tart run`.
type RunOpts struct {
	NetShared  bool       // adds --net-shared (default for our use)
	NoGraphics bool       // adds --no-graphics
	DirMounts  []DirMount // each becomes a --dir=<spec> arg
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
	if opts.NetShared {
		args = append(args, "--net-shared")
	}
	for _, m := range opts.DirMounts {
		args = append(args, t.formatDirArg(m))
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
// stderr, and exit code.
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
