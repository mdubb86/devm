package schema

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var sshAliasPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,252}$`)

// validateSSHTargets checks per-entry shape and in-list uniqueness.
// Does NOT run `ssh -G` or probe the Mac — that happens at apply time
// (see ResolveSSHTargets); here we only reject malformed YAML.
func (c Config) validateSSHTargets() error {
	if c.Project.SSH == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(*c.Project.SSH))
	for i, a := range *c.Project.SSH {
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
		out = append(out, ResolvedSSHTarget{Alias: a, HostName: host, Port: port, User: user})
	}
	return out, nil
}

// CheckKnownHosts asserts each resolved target has at least one
// known_hosts entry on the Mac. Closes the MITM hole: if the user's
// Mac doesn't trust the host yet, the guest can't be the first to
// trust it — the user runs `ssh <alias>` once from the Mac first.
// `run` is `exec.Command(...).Output()` in production.
func CheckKnownHosts(targets []ResolvedSSHTarget, run func(string, ...string) ([]byte, error)) error {
	for _, t := range targets {
		if _, err := run("ssh-keygen", "-F", t.HostName); err != nil {
			return fmt.Errorf(
				"ssh target %q (%s): no known_hosts entry on the Mac. Run `ssh %s` once from your Mac so your known_hosts knows this target, then retry.",
				t.Alias, t.HostName, t.Alias)
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
