package schema

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DefaultExec runs an external command and returns stdout; the
// production default for ResolveSSHTargets and CheckKnownHosts.
// Tests swap this to inject fixtures.
var DefaultExec = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

var sshAliasPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,252}$`)

// validateSSHTargets checks per-entry shape and in-list uniqueness.
// Does NOT run `ssh -G` or probe the Mac — that happens at apply time
// (see ResolveSSHTargets); here we only reject malformed YAML.
func (c Config) validateSSHTargets() error {
	if c.SSH == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(*c.SSH))
	for i, a := range *c.SSH {
		if a == "" {
			return fmt.Errorf("ssh[%d]: empty alias", i)
		}
		if !sshAliasPattern.MatchString(a) {
			return fmt.Errorf("ssh[%d]: %q must match %s", i, a, sshAliasPattern)
		}
		if _, dup := seen[a]; dup {
			return fmt.Errorf("ssh[%d]: duplicate alias %q", i, a)
		}
		seen[a] = struct{}{}
	}
	return nil
}

// ResolvedSSHTarget is the Mac-resolved form of one alias.
type ResolvedSSHTarget struct {
	Alias    string
	HostName string
	Port     int
	User     string
	// HostKeyAlias is the `hostkeyalias` field from `ssh -G`. Empty
	// when the Mac ssh_config has no override — the guest's config then
	// omits HostKeyAlias and OpenSSH falls back to HostName.
	HostKeyAlias string
	// UserKnownHostsFiles are the paths from `ssh -G`'s
	// `userknownhostsfile` directive, each with `~/` expanded. Empty
	// when the directive is unset; CheckKnownHosts treats that as the
	// default pair `~/.ssh/known_hosts` + `~/.ssh/known_hosts2`.
	UserKnownHostsFiles []string
}

// ResolveSSHTargets runs `ssh -G <alias>` for each alias and parses the
// output. `run` is a test seam; production is `exec.Command(name,
// args...).Output()`. Rejects aliases whose resolution shows a
// non-empty proxyjump (v1 doesn't support multi-hop), empty hostname,
// or empty user. The alias string IS the error context — callers
// surface it to the user.
func ResolveSSHTargets(aliases []string, run func(string, ...string) ([]byte, error)) ([]ResolvedSSHTarget, error) {
	out := make([]ResolvedSSHTarget, 0, len(aliases))
	for _, a := range aliases {
		body, err := run("ssh", "-G", a)
		if err != nil {
			return nil, fmt.Errorf("ssh -G alias %q: %w", a, err)
		}
		kv := parseSSHGOutput(body)
		host := kv["hostname"]
		if host == "" {
			return nil, fmt.Errorf("ssh -G alias %q: empty hostname", a)
		}
		user := kv["user"]
		if user == "" {
			return nil, fmt.Errorf("ssh -G alias %q: empty user", a)
		}
		if kv["proxyjump"] != "" && kv["proxyjump"] != "none" {
			return nil, fmt.Errorf("ssh -G alias %q: proxyjump %q — multi-hop chains are not supported in v1; simplify Mac ssh config or wait for v2", a, kv["proxyjump"])
		}
		port, err := strconv.Atoi(kv["port"])
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("ssh -G alias %q: port %q not a valid port", a, kv["port"])
		}
		files, err := parseUserKnownHostsFiles(kv["userknownhostsfile"])
		if err != nil {
			return nil, fmt.Errorf("ssh -G alias %q: userknownhostsfile: %w", a, err)
		}
		out = append(out, ResolvedSSHTarget{
			Alias:               a,
			HostName:            host,
			Port:                port,
			User:                user,
			HostKeyAlias:        kv["hostkeyalias"],
			UserKnownHostsFiles: files,
		})
	}
	return out, nil
}

// LookupKeyAndFiles returns the (key, files) pair OpenSSH itself would
// use to find a host entry for this target. HostKeyAlias wins over
// HostName when set; UserKnownHostsFiles wins over the default pair.
// Both CheckKnownHosts (precondition probe on the Mac) and the guest
// known_hosts emission (sshjump_bundle) use this so the two agree on
// what to look up and where.
func LookupKeyAndFiles(r ResolvedSSHTarget) (string, []string) {
	key := r.HostKeyAlias
	if key == "" {
		key = r.HostName
	}
	files := r.UserKnownHostsFiles
	if len(files) == 0 {
		files = defaultKnownHostsFiles()
	}
	return key, files
}

// CheckKnownHosts asserts each resolved target has at least one
// known_hosts entry on the Mac. Closes the MITM hole: if the user's
// Mac doesn't trust the host yet, the guest can't be the first to
// trust it — the user runs `ssh <alias>` once from the Mac first.
// For each target it tries every file in UserKnownHostsFiles (or the
// default pair when unset) keyed by HostKeyAlias when set else
// HostName. A match in ANY file passes. `run` is
// `exec.Command(...).Output()` in production.
func CheckKnownHosts(targets []ResolvedSSHTarget, run func(string, ...string) ([]byte, error)) error {
	for _, t := range targets {
		key, files := LookupKeyAndFiles(t)
		matched := false
		for _, f := range files {
			if _, err := run("ssh-keygen", "-F", key, "-f", f); err == nil {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf(
				"ssh target %q (lookup key %q): no known_hosts entry on the Mac (tried %s). Run `ssh %s` once from your Mac so your known_hosts knows this target, then retry.",
				t.Alias, key, strings.Join(files, ", "), t.Alias)
		}
	}
	return nil
}

func parseSSHGOutput(body []byte) map[string]string {
	kv := make(map[string]string)
	for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		sp := strings.IndexByte(line, ' ')
		if sp <= 0 {
			continue
		}
		kv[strings.ToLower(line[:sp])] = strings.TrimSpace(line[sp+1:])
	}
	return kv
}

// parseUserKnownHostsFiles splits the `userknownhostsfile` value from
// `ssh -G` into individual paths, strips surrounding double quotes
// around each entry, and expands a leading `~/` to the current user's
// home. A quoted path containing a space is not supported here — if
// one trips a real user we switch to a shlex-style splitter. Empty
// input yields nil (CheckKnownHosts falls back to the default pair).
func parseUserKnownHostsFiles(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Fields(s)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			p = p[1 : len(p)-1]
		}
		expanded, err := expandHome(p)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded)
	}
	return out, nil
}

func expandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

func defaultKnownHostsFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".ssh", "known_hosts"),
		filepath.Join(home, ".ssh", "known_hosts2"),
	}
}
