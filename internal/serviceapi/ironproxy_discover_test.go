package serviceapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseIronProxyProjectID(t *testing.T) {
	cases := []struct {
		name          string
		command       string
		wantProjectID string
		wantOK        bool
	}{
		{
			name:          "canonical path with space in Application Support",
			command:       "/Users/michael/workspace/devm/bin/iron-proxy -config /Users/michael/Library/Application Support/devm/iron-proxy/myproj.yaml",
			wantProjectID: "myproj",
			wantOK:        true,
		},
		{
			name:          "project id with hyphens and dots",
			command:       "/opt/iron-proxy -config /tmp/iron-proxy/foo-bar.baz.yaml",
			wantProjectID: "foo-bar.baz",
			wantOK:        true,
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
			projectID, ok := parseIronProxyProjectID(tc.command)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantProjectID, projectID)
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
	byID := map[string]DiscoveredIronProxy{}
	for _, p := range got {
		byID[p.ProjectID] = p
	}

	assert.Equal(t, 200, byID["projA"].PID)
	assert.Equal(t, 300, byID["projB"].PID)
	assert.NotContains(t, byID, "strangerprojC", "wrong binary path must not be adopted")
	assert.NotContains(t, byID, "bad", "malformed pid line must be skipped")
	assert.Len(t, got, 2)
}

func TestParseIronProxyProcesses_EmptyInput(t *testing.T) {
	got := parseIronProxyProcesses("", "/anywhere/iron-proxy")
	assert.Empty(t, got)
}

func TestLoadIronProxyInfoFromConfig(t *testing.T) {
	// Round-trip: write an IronProxyConfig via YAML(), then read it back
	// via loadIronProxyInfoFromConfig. Pins that the reader stays
	// in sync with the writer — if either shifts, rehydration silently
	// starts returning zero values and the daemon rebuilds nftables
	// for the wrong ports after a restart.
	cfg := IronProxyConfig{
		HTTPListen:  "192.168.64.1:59481",
		HTTPSListen: "192.168.64.1:59482",
		DNSListen:   "192.168.64.1:59483",
		DNSProxyIP:  "192.0.2.1",
		CACertPath:  "/tmp/ca.crt",
		CAKeyPath:   "/tmp/ca.key",
	}
	blob, err := cfg.YAML()
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "proj.yaml")
	require.NoError(t, os.WriteFile(path, blob, 0600))

	info, err := loadIronProxyInfoFromConfig(path)
	require.NoError(t, err)
	assert.Equal(t, ironProxyInfo{
		MacHost:   "192.168.64.1",
		HTTPPort:  59481,
		HTTPSPort: 59482,
		DNSPort:   59483,
	}, info)
}

func TestLoadIronProxyInfoFromConfig_MissingFile(t *testing.T) {
	_, err := loadIronProxyInfoFromConfig("/nonexistent/nowhere.yaml")
	assert.Error(t, err)
}

func TestLoadIronProxyInfoFromConfig_MalformedListen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
dns:
  listen: "not-a-hostport"
proxy:
  http_listen: "192.168.64.1:80"
  https_listen: "192.168.64.1:443"
`), 0600))
	_, err := loadIronProxyInfoFromConfig(path)
	assert.Error(t, err)
}
