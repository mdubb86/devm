// Package sshconfig renders and atomically writes the devm-managed
// ssh_config include file. The user references it once from
// ~/.ssh/config; devm re-emits it on every VM lifecycle event so the
// Host block reflects the currently-running VMs.
package sshconfig

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
)

// safeIdent is the character set allowed in the project name for safe
// rendering into ssh_config. Whitelist over blacklist — blacklisting
// missed space (Host-pattern separator), comma (also a pattern separator),
// hash (line comment), star (wildcard), and control chars across two
// prior fix passes. This regex is the intersection of "characters devm's
// real project names use" and "characters that have no meaning in
// ssh_config". It backstops schema validation as the last layer that
// catches injection attempts.
var safeIdent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// Entry describes one Host block to emit. HostName and Port are no
// longer independent fields — softnet binds every project's guest :22
// on its allocated ProjectIP and DNS answers <Name>.test -> ProjectIP
// (see internal/softnet), so the block always points at "<Name>.test"
// on port 22; nothing daemon-side needs to be fetched to resolve it.
type Entry struct {
	Name           string // project name: host alias devm-<Name> + on-disk path lookups
	KeyPath        string // path to the project's SSH private key
	KnownHostsPath string // path to the project's known_hosts file
}

const header = `# Managed by devm. Regenerated on VM lifecycle events; hand edits will be
# overwritten. Include from ~/.ssh/config as:
#     Include "~/Library/Application Support/devm/ssh_config"

`

const blockTmpl = `Host devm-{{.Name}}
    HostName             {{.Name}}.test
    User                 devm
    Port                 22
    IdentityFile         "{{.KeyPath}}"
    UserKnownHostsFile   "{{.KnownHostsPath}}"
    HostKeyAlias         devm-{{.Name}}
    StrictHostKeyChecking yes
    IdentitiesOnly       yes

`

// Path returns the absolute path devm writes to.
func Path(cfg identity.Config) string {
	return filepath.Join(cfg.RuntimeDir(), "ssh_config")
}

// validateEntry rejects unsafe Name values to prevent ssh_config
// injection attacks (newlines, quotes, control chars, path traversal).
// This is the ONLY layer that validates these constraints — schema
// validation only checks non-empty. KeyPath/KnownHostsPath aren't
// separately validated: Emit derives them from the already-validated
// Name via sshkeys.ProjectDir, so they can't carry attacker input.
func validateEntry(e Entry) error {
	if !safeIdent.MatchString(e.Name) {
		return fmt.Errorf("unsafe name %q: only [a-zA-Z0-9._-] allowed", e.Name)
	}
	if strings.Contains(e.Name, "..") {
		return fmt.Errorf("unsafe name %q: path traversal not allowed", e.Name)
	}
	return nil
}

// Emit atomically replaces the ssh_config file with header + one block
// per entry (sorted by Name ascending).
func Emit(cfg identity.Config, entries []Entry) error {
	filled := make([]Entry, len(entries))
	for i, e := range entries {
		dir := sshkeys.ProjectDir(cfg, e.Name)
		e.KeyPath = filepath.Join(dir, "id_ed25519")
		e.KnownHostsPath = filepath.Join(dir, "known_hosts")
		filled[i] = e
	}

	var buf bytes.Buffer
	if err := emit(&buf, filled); err != nil {
		return err
	}

	dir := cfg.RuntimeDir()
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
	return os.Rename(tmpPath, Path(cfg))
}

// emit is the pure rendering core: header + one validated, sorted block
// per entry, written to w. Split out from Emit so tests can assert on
// rendered content without touching the filesystem.
func emit(w io.Writer, entries []Entry) error {
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	tmpl := template.Must(template.New("block").Parse(blockTmpl))
	for _, e := range sorted {
		if err := validateEntry(e); err != nil {
			return err
		}
		if err := tmpl.Execute(w, e); err != nil {
			return fmt.Errorf("render entry: %w", err)
		}
	}
	return nil
}
