package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mdubb86/devm/internal/schema"
)

// guestSystemPATH is the machine-wide PATH devm sets in /etc/environment.
// Matches Debian's shipped default so root systemd units and direct
// root sessions get sbin — /etc/environment is machine-wide, not
// user-scoped. cfg.Path entries are prepended by RenderEtcEnvironment.
const guestSystemPATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/games"

// caBundlePath is the merged system trust store — Mozilla's set plus
// devm's CA, produced by update-ca-certificates. Every CA-env var
// points here so the same value works both in the guest (where the
// bundle sits at this path) and inside containers (where the shim
// bind-mounts the same bundle at the same path).
//
// Never point CA envs at /usr/local/share/ca-certificates/devm.crt —
// that path only exists in the guest, and for SSL_CERT_FILE/
// REQUESTS_CA_BUNDLE it REPLACES the trust set with just one cert
// (see recipes/lang/uv.md: "The SSL_CERT_FILE trap").
const caBundlePath = "/etc/ssl/certs/ca-certificates.crt"

// bareRunes is the set of chars that appear unquoted in /etc/environment
// AND survive both pam_env and shell `set -a; .` unchanged. Conservative:
// excludes shell metachars (space, |, &, ;, <, >, (, ), quotes, backslash,
// dollar, backtick, #) and any char with ambiguous parsing. Values just
// outside this set get single-quoted (safe in both parsers).
const bareRunes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_/.:@-"

// encodeEtcEnvValue returns v formatted for a single KEY=value line in
// /etc/environment such that BOTH pam_env AND shell `set -a; . /etc/
// environment` produce v verbatim. Returns error if v cannot be safely
// encoded — the caller surfaces the error message to the user (invalid
// devm.yaml value). Contract pinned by internal/render/
// etc_environment_contract_test.go, which round-trips every accept-set
// value through a Debian 13 container matching the devm guest.
//
// Reject set:
//  1. \n, \r, or NUL — pam_env can't represent (line-terminator or
//     C-string terminator).
//  2. '#' anywhere — pam_env treats # as a comment marker even inside
//     quotes; there is no encoding that survives.
//  3. ' combined with $, \, ", or backtick — no encoding survives:
//     the ' wrap can't hold ', and the " wrap would trigger shell
//     escape/expansion/command-substitution while pam_env keeps them
//     literal.
//
// Encoding rules (in order):
//   - Contains ' (guaranteed no $, \, ", or backtick per rejects above)
//     → double-quote: "don't stop" — both parsers strip outer ", no
//     inner char is shell-special so no escape/expansion happens.
//   - Empty string → a pair of single quotes (two ' characters)
//   - Only bare-safe chars per bareRunes → emit bare: KEY=value
//   - Otherwise → single-quote: 'has $ or \ or " or backtick' — single
//     quotes are literal in both parsers.
func encodeEtcEnvValue(v string) (string, error) {
	for _, r := range v {
		switch r {
		case '\n', '\r', 0:
			return "", fmt.Errorf("value contains control character (newline/CR/NUL)")
		case '#':
			return "", fmt.Errorf("value contains '#' — pam_env truncates at '#' even inside quotes")
		}
	}
	hasApos := strings.ContainsRune(v, '\'')
	if hasApos {
		if strings.ContainsAny(v, "$\\\"`") {
			return "", fmt.Errorf("value contains ' combined with one of $ \\ \" or backtick — no encoding survives both pam_env and shell")
		}
		return `"` + v + `"`, nil
	}
	if v == "" {
		return `''`, nil
	}
	bare := true
	for _, r := range v {
		if !strings.ContainsRune(bareRunes, r) {
			bare = false
			break
		}
	}
	if bare {
		return v, nil
	}
	return `'` + v + `'`, nil
}

// RenderEtcEnvironment returns the body of /etc/environment for cfg.
// This is devm's single canonical env-delivery file: reached by pam_env
// (SSH login, `su -`, systemd via EnvironmentFile=) AND by shell
// `set -a; . /etc/environment` (with-devm-env wrapper for devm exec/
// shell, /etc/profile.d/devm.sh for login shells). The contract test
// at etc_environment_contract_test.go pins the round-trip on our target
// guest.
//
// cfg is expected to have been through schema.ResolveEnv so WORKSPACE
// and IS_SANDBOX are in cfg.Env.
//
// Emitted lines, in this order:
//  1. NO_PROXY=*
//  2. CA trust vars — SSL_CERT_FILE, SSL_CERT_DIR, REQUESTS_CA_BUNDLE,
//     CURL_CA_BUNDLE, AWS_CA_BUNDLE, NODE_EXTRA_CA_CERTS all pointing
//     at caBundlePath, plus UV_SYSTEM_CERTS=1.
//  3. PATH=<cfg.Path[0]>:<cfg.Path[1]>:...:/opt/devm/scripts:<guestSystemPATH>
//     — encoded via encodeEtcEnvValue like every other value; bare
//     emission for the common case (all chars bare-safe), single-quoted
//     if a user cfg.Path entry contains non-bare chars.
//  4. cfg.Env entries (including WORKSPACE, IS_SANDBOX), sorted by key.
//  5. Per-service NAME_KEY entries, sorted by name then key.
//
// Returns error if any value fails encodeEtcEnvValue — the caller
// surfaces it to the user (invalid devm.yaml value).
func RenderEtcEnvironment(cfg schema.Config) (string, error) {
	var b strings.Builder

	// Fixed vars (bare-safe, no quoting needed).
	b.WriteString("NO_PROXY=*\n")
	fmt.Fprintf(&b, "SSL_CERT_FILE=%s\n", caBundlePath)
	b.WriteString("SSL_CERT_DIR=/etc/ssl/certs\n")
	fmt.Fprintf(&b, "REQUESTS_CA_BUNDLE=%s\n", caBundlePath)
	fmt.Fprintf(&b, "CURL_CA_BUNDLE=%s\n", caBundlePath)
	fmt.Fprintf(&b, "AWS_CA_BUNDLE=%s\n", caBundlePath)
	fmt.Fprintf(&b, "NODE_EXTRA_CA_CERTS=%s\n", caBundlePath)
	b.WriteString("UV_SYSTEM_CERTS=1\n")

	// PATH — cfg.Path prepended in front of /opt/devm/scripts and
	// guestSystemPATH. cfg.Path is validated + $WORKSPACE-expanded by
	// schema.ResolveEnv. Uses the standard encoder like every other
	// value; the common case (all chars bare-safe) emits bare.
	for i, entry := range cfg.Path {
		if _, err := encodeEtcEnvValue(entry); err != nil {
			return "", fmt.Errorf("encoding cfg.Path[%d] %q: %w", i, entry, err)
		}
	}
	pathParts := make([]string, 0, len(cfg.Path)+2)
	pathParts = append(pathParts, cfg.Path...)
	pathParts = append(pathParts, "/opt/devm/scripts")
	pathParts = append(pathParts, guestSystemPATH)
	pathVal := strings.Join(pathParts, ":")
	pathEncoded, err := encodeEtcEnvValue(pathVal)
	if err != nil {
		return "", fmt.Errorf("encoding PATH: %w", err)
	}
	fmt.Fprintf(&b, "PATH=%s\n", pathEncoded)

	// cfg.Env — sorted, resolved values.
	envKeys := make([]string, 0, len(cfg.Env))
	for k := range cfg.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		q, err := encodeEtcEnvValue(cfg.Env[k].Render())
		if err != nil {
			return "", fmt.Errorf("encoding env %q: %w", k, err)
		}
		fmt.Fprintf(&b, "%s=%s\n", k, q)
	}

	// Per-service prefixed env: SERVICE_KEY. Sort services then keys
	// within each so output is deterministic.
	svcNames := make([]string, 0, len(cfg.Services))
	for n := range cfg.Services {
		svcNames = append(svcNames, n)
	}
	sort.Strings(svcNames)
	for _, name := range svcNames {
		svc := cfg.Services[name]
		upper := strings.ToUpper(name)
		keys := make([]string, 0, len(svc.Env))
		for k := range svc.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			q, err := encodeEtcEnvValue(svc.Env[k].Render())
			if err != nil {
				return "", fmt.Errorf("encoding service %q env %q: %w", name, k, err)
			}
			fmt.Fprintf(&b, "%s_%s=%s\n", upper, k, q)
		}
	}

	return b.String(), nil
}
