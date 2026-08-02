package main

import (
	"strings"

	"github.com/mdubb86/devm/internal/caenv"
)

// containerInheritVars is the set of env vars devm-docker-shim projects
// from /etc/environment into every `docker run`/`create`/`exec`. Sourced
// from caenv.Keys() so this whitelist stays in lockstep with the values
// devm wrote into /etc/environment on the guest (via RenderEtcEnvironment).
// Adding a new CA-trust env var means touching internal/caenv/vars.go only.
//
// The whitelist itself is the security boundary: /etc/environment also
// carries PATH, WORKSPACE, per-service NAME_KEY secrets, and cfg.Env —
// none of which we want to leak into every container. Only keys in this
// list AND present in /etc/environment get injected.
var containerInheritVars = caenv.Keys()

// parseEtcEnvironment parses the KEY=VALUE format used by
// /etc/environment (pam_env-compatible). Handles quoted values
// (double- or single-quoted, matching ends only), ignores blanks and
// comment lines. Malformed lines (no `=`) are silently skipped.
func parseEtcEnvironment(body string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq <= 0 {
			continue
		}
		key := trimmed[:eq]
		val := trimmed[eq+1:]
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		} else if len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'' {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	return out
}

// userEnvKeys extracts the set of env-var keys the user set explicitly
// via `-e KEY=`, `-e KEY`, `--env KEY=`, or `--env=KEY=` flags in argv.
// Used to skip our own injection for any key the user already handled.
func userEnvKeys(argv []string) map[string]struct{} {
	out := make(map[string]struct{})
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-e" || a == "--env":
			if i+1 < len(argv) {
				out[keyFromEnvValue(argv[i+1])] = struct{}{}
				i++ // consume value
			}
		case strings.HasPrefix(a, "--env="):
			out[keyFromEnvValue(strings.TrimPrefix(a, "--env="))] = struct{}{}
		}
	}
	return out
}

// keyFromEnvValue extracts the KEY from "KEY=VALUE" or the whole string
// if no `=` (for `-e KEY` inherit-form).
func keyFromEnvValue(v string) string {
	if eq := strings.IndexByte(v, '='); eq >= 0 {
		return v[:eq]
	}
	return v
}

// containerInheritArgs builds the `-e KEY=VAL` slice to inject before
// exec'ing docker. Includes only keys in containerInheritVars that
// (a) exist in etcEnvBody and (b) aren't already set by the user in argv.
func containerInheritArgs(argv []string, etcEnvBody string) []string {
	env := parseEtcEnvironment(etcEnvBody)
	userSet := userEnvKeys(argv)
	var out []string
	for _, key := range containerInheritVars {
		if _, alreadySet := userSet[key]; alreadySet {
			continue
		}
		val, ok := env[key]
		if !ok {
			continue
		}
		out = append(out, "-e", key+"="+val)
	}
	return out
}
