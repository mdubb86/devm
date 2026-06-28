package serviceapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseConfigProjectID(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
		wantOK  bool
	}{
		{
			name:    "canonical path",
			command: "/Users/michael/workspace/devm/bin/iron-proxy -config /Users/michael/Library/Application Support/devm/iron-proxy/myproj.yaml",
			want:    "myproj",
			wantOK:  true,
		},
		{
			name:    "project id with hyphens and dots",
			command: "/opt/iron-proxy -config /tmp/iron-proxy/foo-bar.baz.yaml",
			want:    "foo-bar.baz",
			wantOK:  true,
		},
		{
			name:    "no /iron-proxy/ in argv",
			command: "/bin/sh -c true",
			wantOK:  false,
		},
		{
			name:    "no .yaml suffix",
			command: "/bin/iron-proxy -config /tmp/iron-proxy/myproj.json",
			wantOK:  false,
		},
		{
			name:    "empty project id",
			command: "/bin/iron-proxy -config /tmp/iron-proxy/.yaml",
			wantOK:  false,
		},
		{
			name:    "binary path component only (no -config arg)",
			command: "/path/iron-proxy --version",
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseConfigProjectID(tc.command)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestParseIronProxyProcesses(t *testing.T) {
	binary := "/Users/michael/workspace/devm/bin/iron-proxy"
	psOutput := `  100 /usr/sbin/syslogd
  200 /Users/michael/workspace/devm/bin/iron-proxy -config /Users/michael/Library/Application Support/devm/iron-proxy/projA.yaml
  300 /Users/michael/workspace/devm/bin/iron-proxy -config /Users/michael/Library/Application Support/devm/iron-proxy/projB.yaml
  400 /opt/homebrew/bin/iron-proxy -config /tmp/iron-proxy/strangerprojC.yaml
  500 /Users/michael/workspace/devm/bin/iron-proxy --help
notanint /Users/michael/workspace/devm/bin/iron-proxy -config /tmp/iron-proxy/bad.yaml
`
	got := parseIronProxyProcesses(psOutput, binary)

	assert.Equal(t, 200, got["projA"])
	assert.Equal(t, 300, got["projB"])
	assert.NotContains(t, got, "strangerprojC", "wrong binary path must not be adopted")
	assert.NotContains(t, got, "bad", "malformed pid line must be skipped")
	assert.Len(t, got, 2)
}

func TestParseIronProxyProcesses_EmptyInput(t *testing.T) {
	got := parseIronProxyProcesses("", "/anywhere/iron-proxy")
	assert.Empty(t, got)
}
