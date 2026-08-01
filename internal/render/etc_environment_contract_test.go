package render

// Encoder-level shape tests for /etc/environment values. The full
// round-trip contract (rendered file → pam_env delivery + shell
// sourcing) lives in e2e/test_135_etc_environment_roundtrip.py,
// which exercises the real devm guest rather than a look-alike
// container.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// acceptCases: values encodeEtcEnvValue must encode without error.
// Kept in sync with ACCEPT_CASES in the e2e test — the encoder is
// the same in both places, and drift here would silently narrow
// coverage.
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

// rejectCases: values encodeEtcEnvValue must reject with a clear error.
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

// TestEncoder_Rejects: accept cases encode without error; reject
// cases error. Pure Go, no external deps.
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
