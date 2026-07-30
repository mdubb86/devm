package render

// Contract test for /etc/environment encoding. Runs a Debian 13
// arm64 container matching the devm guest's PAM/bash stack, writes
// probe values through production encodeEtcEnvValue, and asserts BOTH
// delivery paths — pam_env (SSH/su/systemd sessions) and shell
// `set -a; .` (with-devm-env wrapper, /etc/profile.d/devm.sh) — return
// the exact input bytes.
//
// This is load-bearing: /etc/environment is the single canonical env
// transport, and this test proves both delivery paths decode it
// identically. If it fails, the value has an unencoded/undecoded round
// trip and the consolidation breaks. This test pins production
// RenderEtcEnvironment's encoder (etc_environment.go) directly.
//
// Skips (with reason) if docker is not on PATH. Runs on Linux CI
// where docker is always present and on dev boxes with OrbStack/
// Docker Desktop. Not a unit test — bash+container startup is ~2s.
//
// The container image (debian:trixie) matches the devm guest's OS.
// If the guest OS floor moves (see internal/scripts/install.sh), the
// image here MUST move with it.

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acceptCases are values encodeEtcEnvValue must ROUND-TRIP through both
// pam_env and shell `set -a; .`. Cover common real-world shapes plus
// each edge that survives one of the divergence probes.
var acceptCases = []struct {
	name  string
	value string
}{
	{"bare_alnum", "hello_world"},
	{"bare_path", "/opt/devm/bin"},
	{"bare_url_hostport", "user@host:8080/path.v2-alpha_x"},
	{"bare_empty", ""},
	{"space", "hello world"},
	{"leading_space", " leading"},
	{"trailing_space", "trailing "},
	{"tab", "col1\tcol2"},
	{"apostrophe_only", "don't stop"},
	{"apostrophes_multi", "it's what's happening"},
	{"dollar_only", "cost $50 or ${VAR}"},
	{"backslash_only", "a\\b\\c"},
	{"dquote_only", `he said "hi"`},
	{"backtick_only", "output `cmd` here"},
	{"star_bang_semi", "has * ! ; chars"},
	{"json_like", `{"key":"value","n":1}`},
	{"url_with_query", "https://example.com/a?b=1&c=2"},
	{"unicode", "héllo wörld"},
	{"long_value", strings.Repeat("x", 500)},
}

// rejectCases must be rejected by encodeEtcEnvValue with a clear error.
var rejectCases = []struct {
	name  string
	value string
}{
	{"newline", "line1\nline2"},
	{"cr", "line1\rline2"},
	{"nul", "a\x00b"},
	{"hash_middle", "value#with#hash"},
	{"hash_leading", "#leading"},
	{"hash_trailing", "trailing#"},
	{"apos_and_dollar", "p'ass$word"},
	{"apos_and_backslash", "it's a\\b"},
	{"apos_and_dquote", `it's "quoted"`},
	{"apos_and_backtick", "it's `cmd`"},
}

// TestEncoder_Rejects — Go-only, no docker. Every rejectCases value
// must error; every acceptCases value must encode without error.
func TestEncoder_Rejects(t *testing.T) {
	for _, tc := range acceptCases {
		t.Run("accept/"+tc.name, func(t *testing.T) {
			_, err := encodeEtcEnvValue(tc.value)
			assert.NoError(t, err, "accept case must encode: %q", tc.value)
		})
	}
	for _, tc := range rejectCases {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			_, err := encodeEtcEnvValue(tc.value)
			assert.Error(t, err, "reject case must error: %q", tc.value)
		})
	}
}

// TestEncoder_RoundTripThroughPamAndShell writes every acceptCases
// value (encoded) into a container's /etc/environment, then reads
// each back via both delivery paths, asserting exact byte equality.
// This is the actual contract — if it passes, consolidation to one
// canonical file is safe.
func TestEncoder_RoundTripThroughPamAndShell(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker required for /etc/environment contract test")
	}

	dir := t.TempDir()

	// Build the /etc/environment body.
	var lines []string
	for _, tc := range acceptCases {
		enc, err := encodeEtcEnvValue(tc.value)
		require.NoError(t, err, "encoder failed for %s", tc.name)
		lines = append(lines, "K_"+tc.name+"="+enc)
	}
	envBody := strings.Join(lines, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "etc-environment"), []byte(envBody), 0o644))

	// Probe script: for each K_*, print base64(pam_value) and
	// base64(shell_value) so any binary-safe value round-trips.
	probe := `#!/usr/bin/env bash
set -eu
useradd -m -s /bin/bash devm >/dev/null
cp /work/etc-environment /etc/environment
for key in $(grep -oE '^K_[a-zA-Z0-9_]+' /etc/environment); do
    pam=$(su - devm -c "printenv $key" 2>/dev/null || printf '')
    shell=$(bash -c "set -a; . /etc/environment; set +a; printenv $key" 2>/dev/null || printf '')
    p64=$(printf '%s' "$pam" | base64 -w0)
    s64=$(printf '%s' "$shell" | base64 -w0)
    printf '%s %s %s\n' "$key" "$p64" "$s64"
done
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "probe.sh"), []byte(probe), 0o755))

	// No --platform pin: encoder behavior depends on bash + pam_env text
	// parsing, not on architecture. Host-native arch runs on both arm64
	// dev boxes and amd64 CI without QEMU.
	cmd := exec.Command("docker", "run", "--rm",
		"-v", dir+":/work:ro",
		"debian:trixie", "bash", "/work/probe.sh")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "container run failed:\n%s", string(out))

	pamVals := map[string]string{}
	shellVals := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		// Skip any container-side noise (docker pull progress, useradd
		// warnings, etc.) that isn't a probe-emitted result line. The
		// probe always emits lines matching `^K_\w+ <b64> <b64>$`.
		if !strings.HasPrefix(line, "K_") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		p, err1 := base64.StdEncoding.DecodeString(fields[1])
		require.NoError(t, err1, "pam base64 decode failed for line %q — full container output:\n%s", line, string(out))
		s, err2 := base64.StdEncoding.DecodeString(fields[2])
		require.NoError(t, err2, "shell base64 decode failed for line %q — full container output:\n%s", line, string(out))
		pamVals[fields[0]] = string(p)
		shellVals[fields[0]] = string(s)
	}

	for _, tc := range acceptCases {
		key := "K_" + tc.name
		assert.Equal(t, tc.value, pamVals[key],
			"%s: pam_env round-trip mismatch (input %q, encoded %q)",
			key, tc.value, mustEncode(tc.value))
		assert.Equal(t, tc.value, shellVals[key],
			"%s: shell round-trip mismatch (input %q, encoded %q)",
			key, tc.value, mustEncode(tc.value))
	}
}

func mustEncode(v string) string {
	e, _ := encodeEtcEnvValue(v)
	return e
}
