// Package sshconfig renders and atomically writes the devm-managed
// ssh_config include file. The user references it once from
// ~/.ssh/config; devm re-emits it on every VM lifecycle event so the
// Host block reflects the currently-running VMs.
package sshconfig

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
)

// safeIdent is the character set allowed in VMName + ProjectID for
// safe rendering into ssh_config. Whitelist over blacklist — blacklisting
// missed space (Host-pattern separator), comma (also a pattern separator),
// hash (line comment), star (wildcard), and control chars across two
// prior fix passes. This regex is the intersection of "characters devm's
// real vm_names + project.ids use" and "characters that have no meaning
// in ssh_config". Schema validation only enforces non-empty, so this is
// the ONLY layer that catches injection attempts.
var safeIdent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// Entry describes one Host block to emit.
type Entry struct {
	ProjectID string // for on-disk path lookups (keys, known_hosts)
	VMName    string // the user-facing host alias devm-<VMName>
	VMIP      string // current IP filled at emission time
}

const header = `# Managed by devm. Regenerated on VM lifecycle events; hand edits will be
# overwritten. Include from ~/.ssh/config as:
#     Include "~/Library/Application Support/devm/ssh_config"

`

const blockTmpl = `Host devm-{{.VMName}}
    HostName             {{.VMIP}}
    User                 devm
    Port                 22
    IdentityFile         "{{.KeyPath}}"
    UserKnownHostsFile   "{{.KnownHostsPath}}"
    HostKeyAlias         devm-{{.VMName}}
    StrictHostKeyChecking yes
    IdentitiesOnly       yes

`

// Path returns the absolute path devm writes to.
func Path() string {
	return filepath.Join(serviceapi.RuntimeDir(), "ssh_config")
}

// Emit atomically replaces the ssh_config file with header + one block
// per entry (sorted by VMName ascending). Validates all entry fields to
// prevent ssh_config injection attacks (newlines, quotes, control chars).
// This is the ONLY layer that validates these constraints — schema
// validation only checks non-empty.
func Emit(entries []Entry) error {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].VMName < sorted[j].VMName })

	for _, e := range sorted {
		if !safeIdent.MatchString(e.VMName) {
			return fmt.Errorf("unsafe VMName %q: only [a-zA-Z0-9._-] allowed", e.VMName)
		}
		if !safeIdent.MatchString(e.ProjectID) {
			return fmt.Errorf("unsafe ProjectID %q: only [a-zA-Z0-9._-] allowed", e.ProjectID)
		}
		if strings.Contains(e.ProjectID, "..") {
			return fmt.Errorf("unsafe ProjectID %q: path traversal not allowed", e.ProjectID)
		}
		if net.ParseIP(e.VMIP) == nil {
			return fmt.Errorf("unsafe VMIP %q: not a valid IP address", e.VMIP)
		}
	}

	tmpl := template.Must(template.New("block").Parse(blockTmpl))
	var buf bytes.Buffer
	buf.WriteString(header)
	for _, e := range sorted {
		dir := sshkeys.ProjectDir(e.ProjectID)
		if err := tmpl.Execute(&buf, map[string]string{
			"VMName":         e.VMName,
			"VMIP":           e.VMIP,
			"KeyPath":        filepath.Join(dir, "id_ed25519"),
			"KnownHostsPath": filepath.Join(dir, "known_hosts"),
		}); err != nil {
			return fmt.Errorf("render entry: %w", err)
		}
	}

	dir := serviceapi.RuntimeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "ssh_config.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, Path())
}

// EmitCurrent reads the current state snapshot directory + queries
// tart for running VMs, and emits the intersection. Callers use this
// from lifecycle hooks instead of composing Entry slices manually.
//
// snapshotReader should be a closure that calls serviceapi.ReadStateSnapshot.
// It's passed as a callback to avoid package-level import cycles (sshconfig
// is a sub-package of serviceapi, so it cannot import its parent directly
// in a way that allows the parent to import the sub-package).
func EmitCurrent(ctx context.Context, tr *tart.Tart,
	snapshotReader func(projectID string) (any, error),
	stateDir func() string) error {
	vms, err := tr.List(ctx)
	if err != nil {
		return fmt.Errorf("tart list: %w", err)
	}
	// Build vm-name → running lookup.
	running := make(map[string]bool, len(vms))
	for _, v := range vms {
		if v.Running {
			running[v.Name] = true
		}
	}
	// Walk state dir for known projects.
	entries, err := listStateProjects(stateDir())
	if err != nil {
		return fmt.Errorf("list state projects: %w", err)
	}
	var out []Entry
	for _, projectID := range entries {
		snap, err := snapshotReader(projectID)
		if err != nil || snap == nil {
			continue
		}
		// Extract VMName from the snapshot using reflection.
		// snap should be a *serviceapi.StateSnapshot with field Cfg.Project.VMName.
		snapVal := reflect.ValueOf(snap)
		if snapVal.Kind() != reflect.Ptr {
			continue
		}
		cfg := snapVal.Elem().FieldByName("Cfg")
		if !cfg.IsValid() {
			continue
		}
		project := cfg.FieldByName("Project")
		if !project.IsValid() {
			continue
		}
		vmNameVal := project.FieldByName("VMName")
		if !vmNameVal.IsValid() || vmNameVal.Kind() != reflect.String {
			continue
		}
		vmName := vmNameVal.String()
		if !running[vmName] {
			continue
		}
		ip, err := tr.IP(ctx, vmName)
		if err != nil || ip == "" {
			continue
		}
		out = append(out, Entry{
			ProjectID: projectID,
			VMName:    vmName,
			VMIP:      ip,
		})
	}
	return Emit(out)
}

// listStateProjects lists project IDs devm has state snapshots for.
func listStateProjects(stateDir string) ([]string, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(name, ".json"))
	}
	return out, nil
}
