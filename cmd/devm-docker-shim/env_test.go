package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseEtcEnvironment(t *testing.T) {
	body := `# comment line
NO_PROXY=*
NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt
PATH="/opt/devm/scripts:/usr/local/bin:/usr/bin"

WORKSPACE="/workspace/foo"
UV_SYSTEM_CERTS=1
`
	got := parseEtcEnvironment(body)
	assert.Equal(t, "*", got["NO_PROXY"])
	assert.Equal(t, "/etc/ssl/certs/ca-certificates.crt", got["NODE_EXTRA_CA_CERTS"])
	assert.Equal(t, "/opt/devm/scripts:/usr/local/bin:/usr/bin", got["PATH"]) // quotes stripped
	assert.Equal(t, "/workspace/foo", got["WORKSPACE"])
	assert.Equal(t, "1", got["UV_SYSTEM_CERTS"])
	_, hasComment := got["#"]
	assert.False(t, hasComment)
}

func TestParseEtcEnvironment_SingleQuoted(t *testing.T) {
	body := "TEST_SPACE='hello world'\nTEST_DOLLAR='cost $50'\n"
	got := parseEtcEnvironment(body)
	assert.Equal(t, "hello world", got["TEST_SPACE"])
	assert.Equal(t, "cost $50", got["TEST_DOLLAR"])
}

func TestParseEtcEnvironment_Malformed_Skipped(t *testing.T) {
	body := "GOOD=value\nno_equals_here\nALSO_GOOD=x\n"
	got := parseEtcEnvironment(body)
	assert.Equal(t, "value", got["GOOD"])
	assert.Equal(t, "x", got["ALSO_GOOD"])
	assert.Len(t, got, 2)
}

func TestUserEnvKeys_HandlesAllFlagForms(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{
			name: "-e KEY=VALUE",
			argv: []string{"run", "-e", "FOO=bar", "img"},
			want: []string{"FOO"},
		},
		{
			name: "-e KEY (inherit)",
			argv: []string{"run", "-e", "FOO", "img"},
			want: []string{"FOO"},
		},
		{
			name: "--env KEY=VALUE",
			argv: []string{"run", "--env", "FOO=bar", "img"},
			want: []string{"FOO"},
		},
		{
			name: "--env=KEY=VALUE",
			argv: []string{"run", "--env=FOO=bar", "img"},
			want: []string{"FOO"},
		},
		{
			name: "multiple mixed",
			argv: []string{"run", "-e", "FOO=1", "--env", "BAR=2", "--env=BAZ=3", "img"},
			want: []string{"FOO", "BAR", "BAZ"},
		},
		{
			name: "no env flags",
			argv: []string{"run", "-it", "img"},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := userEnvKeys(tc.argv)
			var gotKeys []string
			for k := range got {
				gotKeys = append(gotKeys, k)
			}
			assert.ElementsMatch(t, tc.want, gotKeys)
		})
	}
}

func TestContainerInheritArgs_InjectsOnlyKnownAndUnset(t *testing.T) {
	body := `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/foo
UV_SYSTEM_CERTS=1
NO_PROXY=*
CFG_UNRELATED=leakme
`
	// User already set NODE_EXTRA_CA_CERTS.
	argv := []string{"run", "-e", "NODE_EXTRA_CA_CERTS=/user/path", "img"}
	got := containerInheritArgs(argv, body)

	// Must contain UV_SYSTEM_CERTS + NO_PROXY, not NODE_EXTRA_CA_CERTS (user set),
	// not CFG_UNRELATED (not in opt-in list).
	assertContainsPair(t, got, "UV_SYSTEM_CERTS", "1")
	assertContainsPair(t, got, "NO_PROXY", "*")
	assertNoKey(t, got, "NODE_EXTRA_CA_CERTS")
	assertNoKey(t, got, "CFG_UNRELATED")
}

// TestContainerInheritArgs_ProjectsAllPerLibraryEnvs pins the per-library
// CA env vars — the ones added specifically because their tool ignores
// SSL_CERT_FILE / REQUESTS_CA_BUNDLE. A regression here (removing one
// from caenv.Vars, or breaking caenv.Keys ordering) would silently drop
// CA trust for that tool inside every container.
func TestContainerInheritArgs_ProjectsAllPerLibraryEnvs(t *testing.T) {
	body := `HTTPLIB2_CA_CERTS=/etc/ssl/certs/ca-certificates.crt
GRPC_DEFAULT_SSL_ROOTS_FILE_PATH=/etc/ssl/certs/ca-certificates.crt
GIT_SSL_CAINFO=/etc/ssl/certs/ca-certificates.crt
CARGO_HTTP_CAINFO=/etc/ssl/certs/ca-certificates.crt
PIP_CERT=/etc/ssl/certs/ca-certificates.crt
`
	got := containerInheritArgs([]string{"run", "img"}, body)
	assertContainsPair(t, got, "HTTPLIB2_CA_CERTS", "/etc/ssl/certs/ca-certificates.crt")
	assertContainsPair(t, got, "GRPC_DEFAULT_SSL_ROOTS_FILE_PATH", "/etc/ssl/certs/ca-certificates.crt")
	assertContainsPair(t, got, "GIT_SSL_CAINFO", "/etc/ssl/certs/ca-certificates.crt")
	assertContainsPair(t, got, "CARGO_HTTP_CAINFO", "/etc/ssl/certs/ca-certificates.crt")
	assertContainsPair(t, got, "PIP_CERT", "/etc/ssl/certs/ca-certificates.crt")
}

func TestContainerInheritArgs_SkipsKeysMissingFromEtcEnv(t *testing.T) {
	body := "NODE_EXTRA_CA_CERTS=/x\n" // only one of the opt-in keys is present
	got := containerInheritArgs([]string{"run", "img"}, body)
	assertContainsPair(t, got, "NODE_EXTRA_CA_CERTS", "/x")
	// SSL_CERT_FILE, UV_SYSTEM_CERTS, etc. not present → not injected
	assertNoKey(t, got, "SSL_CERT_FILE")
	assertNoKey(t, got, "UV_SYSTEM_CERTS")
}

// assertContainsPair checks that ["-e", "KEY=VALUE"] appears consecutively in argv.
func assertContainsPair(t *testing.T, argv []string, key, val string) {
	t.Helper()
	want := key + "=" + val
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-e" && argv[i+1] == want {
			return
		}
	}
	t.Errorf("expected -e %s in %v", want, argv)
}

func assertNoKey(t *testing.T, argv []string, key string) {
	t.Helper()
	prefix := key + "="
	for _, a := range argv {
		if a == key || (len(a) > len(prefix) && a[:len(prefix)] == prefix) {
			t.Errorf("did not expect %s in %v (found %q)", key, argv, a)
			return
		}
	}
}
