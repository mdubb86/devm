// Package sshconfig renders and atomically writes the devm-managed
// ssh_config include file. The user references it once from
// ~/.ssh/config; devm re-emits it on every VM lifecycle event so the
// Host block reflects the currently-running VMs.
package sshconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
)

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
// per entry (sorted by VMName ascending). Rejects entries with unsafe
// vm names (whitespace, control characters) as defense-in-depth even
// though schema.Project.Validate already gates.
func Emit(entries []Entry) error {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].VMName < sorted[j].VMName })

	for _, e := range sorted {
		if strings.ContainsAny(e.VMName, " \t\n\r") || e.VMName == "" {
			return fmt.Errorf("unsafe VMName %q: whitespace or empty not allowed", e.VMName)
		}
		if strings.ContainsAny(e.ProjectID, "/\\") || strings.Contains(e.ProjectID, "..") || e.ProjectID == "" {
			return fmt.Errorf("unsafe ProjectID %q", e.ProjectID)
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
