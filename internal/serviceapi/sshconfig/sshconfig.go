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
// on its allocated ProjectIP and DNS answers <Name>.<TLD> -> ProjectIP
// (see internal/softnet), so the block always points at "<Name>.<TLD>"
// on port 22 (TLD from the emitting identity.Config); nothing
// daemon-side needs to be fetched to resolve it.
type Entry struct {
	Name           string // project name: host alias devm-<Name> + on-disk path lookups
	KeyPath        string // path to the project's SSH private key
	KnownHostsPath string // path to the project's known_hosts file
}

// header returns the comment block written atop cfg's ssh_config file.
// The Include example points at cfg's own Path(cfg) — under E2E that's
// a different runtime dir than Prod's, so the two identities must not
// share this string.
func header(cfg identity.Config) string {
	return fmt.Sprintf(`# Managed by devm. Regenerated on VM lifecycle events; hand edits will be
# overwritten. Include from ~/.ssh/config as:
#     Include "%s"

`, Path(cfg))
}

// blockData is the per-entry template data: Entry plus the TLD sourced
// from identity.Config, so the same Entry can render under Prod
// ("<name>.test") or E2E ("<name>.e2e.test") depending on which
// identity emitted it.
type blockData struct {
	Entry
	TLD string
}

const blockTmpl = `Host devm-{{.Name}}
    HostName             {{.Name}}.{{.TLD}}
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
// per entry (sorted by Name ascending). No-op if cfg.RuntimeDir()
// doesn't exist — the daemon is uninstalled and there's nothing to
// write into; MkdirAll'ing here would resurrect state that
// `devm uninstall` just removed (a `devm teardown` chained after
// `devm uninstall` would otherwise recreate the runtime dir + ssh_config).
// Every legitimate caller runs against a live daemon, which has
// already ensured the runtime dir at startup.
func Emit(cfg identity.Config, entries []Entry) error {
	filled := make([]Entry, len(entries))
	for i, e := range entries {
		kdir := sshkeys.ProjectDir(cfg, e.Name)
		e.KeyPath = filepath.Join(kdir, "id_ed25519")
		e.KnownHostsPath = filepath.Join(kdir, "known_hosts")
		filled[i] = e
	}

	var buf bytes.Buffer
	if err := emit(&buf, cfg, filled); err != nil {
		return err
	}

	// After validation (emit rejects unsafe names), but before the
	// write: if the daemon's runtime dir doesn't exist, no-op. Keeps
	// the security-check contract from TestEmit_RejectsUnsafeName while
	// preventing a stop/teardown chained after uninstall from
	// resurrecting the runtime dir + ssh_config.
	dir := cfg.RuntimeDir()
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", dir, err)
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

// includeLine returns the exact `Include "<path>"` line devm writes
// into (and matches against) ~/.ssh/config for cfg. Deliberately not
// fmt.Sprintf("%q", ...) — Go's %q backslash-escapes characters that a
// hand-typed ssh_config Include line never would, so %q could fail to
// match a line the user typed themselves.
func includeLine(cfg identity.Config) string {
	return `Include "` + Path(cfg) + `"`
}

// userSSHConfigPath returns ~/.ssh/config for the current user.
func userSSHConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// EnsureInclude appends `Include "<Path(cfg)>"` to ~/.ssh/config if not
// already present, so the user's ssh config picks up devm's generated
// Host blocks. Creates ~/.ssh (0700) and ~/.ssh/config (0600) if either
// is missing. Idempotent: a second call after a successful first call
// is a no-op. Any other content in ~/.ssh/config is preserved verbatim;
// matching is by trimmed whole-line equality, not substring, so it
// can't false-match a similar-looking Include line.
func EnsureInclude(cfg identity.Config) error {
	path, err := userSSHConfigPath()
	if err != nil {
		return err
	}
	line := includeLine(cfg)

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read %s: %w", path, err)
		}
		data = nil
	} else {
		for _, l := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(l) == line {
				return nil // already present
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}

	var buf bytes.Buffer
	buf.Write(data)
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		buf.WriteByte('\n')
	}
	buf.WriteString(line)
	buf.WriteByte('\n')

	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// RemoveInclude deletes the `Include "<Path(cfg)>"` line for cfg from
// ~/.ssh/config, leaving every other line untouched. No-op if the file
// doesn't exist or the line isn't present. Matching is by trimmed
// whole-line equality, mirroring EnsureInclude, so a manually-added
// Include line for a different (but textually similar) path is never
// touched.
func RemoveInclude(cfg identity.Config) error {
	path, err := userSSHConfigPath()
	if err != nil {
		return err
	}
	line := includeLine(cfg)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	found := false
	for _, l := range lines {
		if strings.TrimSpace(l) == line {
			found = true
			continue
		}
		out = append(out, l)
	}
	if !found {
		return nil
	}

	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// emit is the pure rendering core: header + one validated, sorted block
// per entry, written to w. Split out from Emit so tests can assert on
// rendered content without touching the filesystem. cfg.TLD selects the
// HostName suffix ("test" under Prod, "e2e.test" under E2E) so entries
// render correctly under whichever identity is emitting them.
func emit(w io.Writer, cfg identity.Config, entries []Entry) error {
	if _, err := io.WriteString(w, header(cfg)); err != nil {
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
		if err := tmpl.Execute(w, blockData{Entry: e, TLD: cfg.TLD}); err != nil {
			return fmt.Errorf("render entry: %w", err)
		}
	}
	return nil
}
