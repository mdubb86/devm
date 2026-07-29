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

// bareRunes is the set of characters that can appear in an unquoted
// pam_env value. Conservative: everything outside this set gets
// double-quoted. Excludes shell/pam_env special characters ($, #,
// space, quotes, backslash) and anything ambiguous. Callers that need
// broader freedom fall through the quoted path.
const bareRunes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_/.:@-"

// etcEnvironmentQuote encodes v for a single KEY=value line in
// /etc/environment. Returns bare when v matches [A-Za-z0-9_/.:@-]*,
// otherwise returns a double-quoted form with \, ", \n, \t escaped.
// Returns an error for values containing raw newline, carriage return,
// or NUL — pam_env can't safely represent them.
func etcEnvironmentQuote(v string) (string, error) {
	for _, r := range v {
		if r == '\n' || r == '\r' || r == 0 {
			return "", fmt.Errorf("value contains unsupported control character (newline/CR/NUL)")
		}
	}
	if v == "" {
		return `""`, nil
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
	var b strings.Builder
	b.Grow(len(v) + 2)
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// RenderEtcEnvironment returns the body of /etc/environment for cfg.
// Same source as RenderEnv (which produces the shell-format /opt/devm/.env)
// — cfg is expected to have been through schema.ResolveEnv so WORKSPACE
// and IS_SANDBOX are in cfg.Env.
//
// Emitted lines, in this order:
//  1. NO_PROXY=*
//  2. CA trust vars — SSL_CERT_FILE, SSL_CERT_DIR, REQUESTS_CA_BUNDLE,
//     CURL_CA_BUNDLE, AWS_CA_BUNDLE, NODE_EXTRA_CA_CERTS all pointing
//     at caBundlePath, plus UV_SYSTEM_CERTS=1. Kept as one block so
//     Python (ssl/httpx/requests), Node, curl, AWS SDKs, and uv all
//     trust the merged bundle without per-project env config.
//  3. PATH="<cfg.Path[0]>:<cfg.Path[1]>:...:/opt/devm/scripts:<guestSystemPATH>"
//     (always double-quoted; cfg.Path may be empty, in which case the
//     join begins at /opt/devm/scripts). PATH is always emitted
//     double-quoted — matching Debian's shipped /etc/environment
//     convention — even when its characters would allow bare emission
//     per etcEnvironmentQuote.
//  4. cfg.Env entries (including WORKSPACE, IS_SANDBOX), sorted by key
//  5. Per-service NAME_KEY entries, sorted by name then key
//
// Returns error if any value contains a raw newline/CR/NUL — the caller
// should surface it to the user (invalid devm.yaml value).
func RenderEtcEnvironment(cfg schema.Config) (string, error) {
	var b strings.Builder

	// Fixed vars (bare, no quoting needed).
	b.WriteString("NO_PROXY=*\n")
	fmt.Fprintf(&b, "SSL_CERT_FILE=%s\n", caBundlePath)
	b.WriteString("SSL_CERT_DIR=/etc/ssl/certs\n")
	fmt.Fprintf(&b, "REQUESTS_CA_BUNDLE=%s\n", caBundlePath)
	fmt.Fprintf(&b, "CURL_CA_BUNDLE=%s\n", caBundlePath)
	fmt.Fprintf(&b, "AWS_CA_BUNDLE=%s\n", caBundlePath)
	fmt.Fprintf(&b, "NODE_EXTRA_CA_CERTS=%s\n", caBundlePath)
	b.WriteString("UV_SYSTEM_CERTS=1\n")

	// PATH — cfg.Path entries prepended in front of /opt/devm/scripts
	// and guestSystemPATH. cfg.Path is already validated + $WORKSPACE-
	// expanded by schema.ResolveEnv.
	pathParts := make([]string, 0, len(cfg.Path)+2)
	pathParts = append(pathParts, cfg.Path...)
	pathParts = append(pathParts, "/opt/devm/scripts")
	pathParts = append(pathParts, guestSystemPATH)
	pathVal := strings.Join(pathParts, ":")
	quoted, err := etcEnvironmentQuote(pathVal)
	if err != nil {
		return "", fmt.Errorf("encoding PATH: %w", err)
	}
	// PATH is always double-quoted in /etc/environment, matching the
	// convention Debian ships in the file's default contents — even
	// though PATH's characters (letters, digits, /, :, -, ., _) fall
	// within bareRunes and etcEnvironmentQuote would otherwise leave
	// it unquoted. Bare output never contains ", \, or control chars,
	// so wrapping it here is safe.
	if !strings.HasPrefix(quoted, `"`) {
		quoted = `"` + quoted + `"`
	}
	fmt.Fprintf(&b, "PATH=%s\n", quoted)

	// cfg.Env — sorted, resolved values.
	envKeys := make([]string, 0, len(cfg.Env))
	for k := range cfg.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		q, err := etcEnvironmentQuote(cfg.Env[k].Render())
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
			q, err := etcEnvironmentQuote(svc.Env[k].Render())
			if err != nil {
				return "", fmt.Errorf("encoding service %q env %q: %w", name, k, err)
			}
			fmt.Fprintf(&b, "%s_%s=%s\n", upper, k, q)
		}
	}

	return b.String(), nil
}
