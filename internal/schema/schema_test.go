package schema

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// ---------- EnvValue / SecretRef tests ----------

func TestMaskRequiredFields(t *testing.T) {
	m := Mask{Path: "node_modules", Size: "2G"}
	assert.NoError(t, m.Validate())

	missingPath := Mask{Size: "2G"}
	assert.Error(t, missingPath.Validate())

	missingSize := Mask{Path: "node_modules"}
	assert.Error(t, missingSize.Validate())
}

func TestMaskPathRejectsAbsolute(t *testing.T) {
	// Masks live under the workspace; absolute paths escape it.
	m := Mask{Path: "/etc/foo", Size: "1G"}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "relative")
}

func TestMaskPathRejectsExpansionVariables(t *testing.T) {
	// No $WORKSPACE expansion happens for mask paths (the renderer
	// already prepends repoRoot). Silent acceptance produces a broken
	// mount at <repoRoot>/$WORKSPACE/... — reject with a hint.
	cases := []string{
		"$WORKSPACE/ts/node_modules",
		"${WORKSPACE}/ts/node_modules",
		"$HOME/foo",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			err := Mask{Path: p, Size: "1G"}.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "relative")
		})
	}
}

func TestMaskPathRejectsHomeShortcut(t *testing.T) {
	// `~` isn't expanded for masks. Reject for the same reason as $.
	err := Mask{Path: "~/foo", Size: "1G"}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "relative")
}

func TestMaskPathRejectsTraversal(t *testing.T) {
	// `../escape` walks outside the repo root — masks must stay inside.
	cases := []string{
		"../escape",
		"..",
		"foo/../../escape",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			err := Mask{Path: p, Size: "1G"}.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "traversal")
		})
	}
}

func TestMaskPathAllowsCleanRelative(t *testing.T) {
	// Nested relative paths, dot-prefixed paths, and inert traversal
	// (a/../b → b) all clean to valid repo-relative paths and must pass.
	cases := []string{
		"node_modules",
		"ts/node_modules",
		"./node_modules",
		"py/.venv",
		"a/../b",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			assert.NoError(t, Mask{Path: p, Size: "1G"}.Validate())
		})
	}
}

func TestServiceValidate(t *testing.T) {
	// Minimum valid: just canonical port
	s := Service{Port: 3000}
	assert.NoError(t, s.Validate())

	// Workspace pseudo-service: no port OK, but must have at least one mask
	workspace := Service{Masks: []Mask{{Path: "node_modules", Size: "2G"}}}
	assert.NoError(t, workspace.Validate())

	// exec-only service: no port, no masks, just exec
	execOnly := Service{Exec: []string{"/usr/bin/redis-server"}}
	assert.NoError(t, execOnly.Validate())

	emptyWorkspace := Service{}
	assert.Error(t, emptyWorkspace.Validate(), "service must have canonical, mask, exec, or systemd")
}

func TestServiceHostnameMustEndInDotTest(t *testing.T) {
	cases := []struct {
		name  string
		host  string
		fails bool
	}{
		{"empty is OK", "", false},
		{"plain .test", "app.test", false},
		{"deep subdomain", "a.b.c.app.test", false},
		{".local rejected", "app.local", true},
		{".dev rejected", "app.dev", true},
		{"no TLD rejected", "app", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Service{Hostname: c.host, Port: 8080}
			err := s.Validate()
			if c.fails {
				assert.Error(t, err)
				if err != nil {
					assert.Contains(t, err.Error(), ".test")
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	c := Config{
		Project: Project{
			ID:          "test",
			SandboxName: "test-sbx",
		},
		Network: Network{Allow: []string{"github.com"}},
		Services: map[string]Service{
			"webapp": {Port: 3000, Hostname: "test.test"},
		},
	}
	assert.NoError(t, c.Validate())

	// Hostname collision across services
	dup := c
	dup.Services = map[string]Service{
		"webapp": {Port: 3000, Hostname: "test.test"},
		"api":    {Port: 8080, Hostname: "test.test"},
	}
	assert.Error(t, dup.Validate(), "duplicate hostname")

	// Port collision across services
	dup2 := Config{
		Project: c.Project,
		Network: c.Network,
		Services: map[string]Service{
			"a": {Port: 3000},
			"b": {Port: 3000},
		},
	}
	assert.Error(t, dup2.Validate(), "duplicate canonical port")

	// Missing required project fields
	bad := c
	bad.Project.ID = ""
	assert.Error(t, bad.Validate(), "project.id required")
}

func TestConfigValidatesPortRange(t *testing.T) {
	base := Config{
		Project: Project{ID: "p", SandboxName: "p"},
	}

	// canonical out of range (too large) → error.
	bigPort := base
	bigPort.Services = map[string]Service{"api": {Port: 70000}}
	assert.Error(t, bigPort.Validate(), "canonical over 65535 must error")

	// Valid port → no error.
	ok := base
	ok.Services = map[string]Service{"api": {Port: 8080}}
	assert.NoError(t, ok.Validate())
}

func TestConfigValidatesInstallSteps(t *testing.T) {
	cfg := Config{
		Project: Project{ID: "x", SandboxName: "x-sbx"},
		Install: []string{
			"", // invalid
		},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install[0]")
}

func TestResolveMount(t *testing.T) {
	root := "/proj"
	cases := []struct {
		name, entry, want string
	}{
		{"absolute", "/etc/hosts", "/etc/hosts"},
		{"absolute_ro", "/etc/hosts:ro", "/etc/hosts:ro"},
		{"relative", "configs/extra", "/proj/configs/extra"},
		{"relative_ro", "configs/extra:ro", "/proj/configs/extra:ro"},
		{"dotdot", "../sibling", "/sibling"},
		{"clean_doubleslash", "/etc//hosts", "/etc/hosts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveMount(tc.entry, root)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveMountTildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	got, err := ResolveMount("~/.ssh", "/proj")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".ssh"), got)

	gotRO, err := ResolveMount("~/.ssh:ro", "/proj")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".ssh")+":ro", gotRO)

	gotBare, err := ResolveMount("~", "/proj")
	require.NoError(t, err)
	assert.Equal(t, home, gotBare)
}

func TestResolveMountErrors(t *testing.T) {
	_, err := ResolveMount("", "/proj")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")

	_, err = ResolveMount(":ro", "/proj")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host path is empty")
}

// TestServicePortPolymorphicUnmarshal exercises the single-field `port:`
// polymorphic decode that accepts either an int (just sandbox port) or
// a "IP:PORT" string (interface + sandbox port).
func TestServicePortPolymorphicUnmarshal(t *testing.T) {
	cases := []struct {
		name      string
		yaml      string
		wantPort  int
		wantBind  string
	}{
		{"int_form", "port: 80", 80, ""},
		{"string_localhost", `port: "127.0.0.1:80"`, 80, "127.0.0.1"},
		{"string_all_interfaces", `port: "0.0.0.0:8080"`, 8080, "0.0.0.0"},
		{"string_specific_ip", `port: "192.168.1.10:5432"`, 5432, "192.168.1.10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s Service
			require.NoError(t, yaml.Unmarshal([]byte(tc.yaml), &s))
			assert.Equal(t, tc.wantPort, s.Port)
			assert.Equal(t, tc.wantBind, s.BindIP)
		})
	}
}

// TestServicePortPolymorphicUnmarshalErrors checks rejected forms.
func TestServicePortPolymorphicUnmarshalErrors(t *testing.T) {
	cases := []struct {
		name      string
		yaml      string
		wantInErr string
	}{
		{"bad_ip", `port: "not-an-ip:80"`, "valid IP"},
		{"no_colon_string", `port: "abc"`, "IP:PORT"},
		{"non_numeric_port", `port: "0.0.0.0:eighty"`, "not an integer"},
		{"list_form_rejected", "port: [1, 2]", "integer or"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s Service
			err := yaml.Unmarshal([]byte(tc.yaml), &s)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantInErr)
		})
	}
}

// TestServiceMarshalRoundTrip pins that a Service with a bound IP
// round-trips through YAML in the polymorphic string form, while a
// bare port round-trips as an int. Snapshot diff requires this.
func TestServiceMarshalRoundTrip(t *testing.T) {
	bound := Service{Port: 80, BindIP: "0.0.0.0"}
	out, err := yaml.Marshal(bound)
	require.NoError(t, err)
	assert.Contains(t, string(out), `port: 0.0.0.0:80`)

	bare := Service{Port: 8080}
	out, err = yaml.Marshal(bare)
	require.NoError(t, err)
	assert.Contains(t, string(out), `port: 8080`)
	assert.NotContains(t, string(out), `bind`)
}

func TestServiceValidatePortBindCoupling(t *testing.T) {
	// BindIP without Port is invalid.
	bad := Service{BindIP: "0.0.0.0"}
	err := bad.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bind interface requires")
}

func TestServiceResolveBind(t *testing.T) {
	assert.Equal(t, "127.0.0.1", Service{Port: 80}.ResolveBind())
	assert.Equal(t, "0.0.0.0", Service{Port: 80, BindIP: "0.0.0.0"}.ResolveBind())
	assert.Equal(t, "192.168.1.10", Service{Port: 5432, BindIP: "192.168.1.10"}.ResolveBind())
}

func TestConfigValidateRejectsEmptyMountEntry(t *testing.T) {
	cfg := Config{
		Project: Project{ID: "x", SandboxName: "x-sbx"},
		Mounts:  []string{"/etc/hosts", ""},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mounts[1]")
}

func TestConfigValidateWithRootChecksExistence(t *testing.T) {
	tmp := t.TempDir()
	existing := filepath.Join(tmp, "real")
	require.NoError(t, os.MkdirAll(existing, 0o755))

	// Existing path passes.
	cfg := Config{
		Project: Project{ID: "x", SandboxName: "x-sbx"},
		Mounts:  []string{existing + ":ro"},
	}
	require.NoError(t, cfg.ValidateWithRoot(tmp))

	// Missing path fails.
	cfg.Mounts = []string{filepath.Join(tmp, "does-not-exist")}
	err := cfg.ValidateWithRoot(tmp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mounts[0]")

	// Relative path resolves against projectRoot.
	relCfg := Config{
		Project: Project{ID: "x", SandboxName: "x-sbx"},
		Mounts:  []string{"real:ro"},
	}
	require.NoError(t, relCfg.ValidateWithRoot(tmp))
}

func TestProject_Proxy_Validation(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"empty defaults to caddy", "", false},
		{"caddy is valid", "caddy", false},
		{"none is valid", "none", false},
		{"unknown is invalid", "nginx", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Project{ID: "x", SandboxName: "x", Proxy: c.value}
			err := p.Validate()
			if c.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCheckLegacyKeys_AllowedDomainsMigration(t *testing.T) {
	yaml := []byte(`
project:
  id: x
  sandbox_name: x-sbx
network:
  allowed_domains:
    - example.com
`)
	err := CheckLegacyKeys(yaml)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network.allowed_domains is no longer supported")
	assert.Contains(t, err.Error(), "network.allow")
}

func TestProject_HostnameApex_MigrationError(t *testing.T) {
	yamlBlob := []byte(`
project:
  id: foo
  sandbox_name: foo-sbx
  hostname_apex: foo.local
`)
	err := CheckLegacyKeys(yamlBlob)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostname_apex is no longer supported")
	assert.Contains(t, err.Error(), "HOSTNAME_APEX")
}

func TestCheckUnknownKeys_TopLevel_Rejected(t *testing.T) {
	yamlBlob := []byte(`
project:
  id: foo
  sandbox_name: foo-sbx
volumes:
  /data: 1G
`)
	err := CheckUnknownKeys(yamlBlob)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field`)
	assert.Contains(t, err.Error(), `volumes`)
	// Valid fields should be listed (so the user knows what IS allowed).
	assert.Contains(t, err.Error(), `services`)
}

func TestCheckUnknownKeys_ProjectLevel_Rejected(t *testing.T) {
	yamlBlob := []byte(`
project:
  id: foo
  sandbox_name: foo-sbx
  proxie: caddy
`)
	err := CheckUnknownKeys(yamlBlob)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field`)
	assert.Contains(t, err.Error(), `proxie`)
}

func TestCheckUnknownKeys_AllValidFields_Accepted(t *testing.T) {
	yamlBlob := []byte(`
project:
  id: foo
  sandbox_name: foo-sbx
  proxy: caddy
base_image: {}
network:
  allow: [github.com]
env:
  EDITOR: vim
services:
  api:
    port: 8080
install:
  - true
mounts:
  - ~/.aws:ro
path:
  - $WORKSPACE/bin
packages:
  - jq
`)
	require.NoError(t, CheckUnknownKeys(yamlBlob))
}

func TestCheckUnknownKeys_EmptyAndMinimal_Accepted(t *testing.T) {
	require.NoError(t, CheckUnknownKeys([]byte("")))
	require.NoError(t, CheckUnknownKeys([]byte(`project:
  id: foo
  sandbox_name: foo-sbx
`)))
}

func TestTemplateValidate(t *testing.T) {
	// Valid.
	ok := Template{Source: "configs/foo.tmpl", Output: "/etc/foo"}
	assert.NoError(t, ok.Validate())

	// Missing source.
	assert.Error(t, Template{Output: "/etc/foo"}.Validate())

	// Missing output.
	assert.Error(t, Template{Source: "foo.tmpl"}.Validate())

	// Path traversal in source — rejected.
	bad := Template{Source: "../etc/passwd", Output: "/etc/foo"}
	err := bad.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")

	// Cleaned form still escapes — rejected.
	bad2 := Template{Source: "configs/../../etc/passwd", Output: "/etc/foo"}
	assert.Error(t, bad2.Validate())

	// Output must be absolute.
	rel := Template{Source: "foo.tmpl", Output: "etc/foo"}
	err = rel.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

func TestService_SystemdOverride_ExclusiveWithDeclarative(t *testing.T) {
	s := Service{
		Systemd: "[Unit]\n[Service]\nExecStart=/bin/true",
		Exec:    []string{"/bin/true"},
	}
	err := s.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestService_Restart_ValidValues(t *testing.T) {
	cases := []struct {
		val string
		ok  bool
	}{
		{"", true},
		{"no", true},
		{"on-failure", true},
		{"always", true},
		{"yes", false},
		{"sometimes", false},
	}
	for _, c := range cases {
		t.Run(c.val, func(t *testing.T) {
			s := Service{Exec: []string{"/bin/true"}, Restart: c.val}
			err := s.Validate()
			if c.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "restart")
			}
		})
	}
}

func TestConfig_MaskMustBeInsideShare(t *testing.T) {
	cfg := Config{
		Project: Project{ID: "x", SandboxName: "x-sbx"},
		Services: map[string]Service{
			"api": {
				Exec:  []string{"/bin/true"},
				Masks: []Mask{{Path: "node_modules", Size: "1G"}},
			},
		},
	}
	require.NoError(t, cfg.Validate(), "relative path is workspace-relative; should pass")

	cfg.Services["api"] = Service{
		Exec:  []string{"/bin/true"},
		Masks: []Mask{{Path: "../escape", Size: "1G"}},
	}
	require.Error(t, cfg.Validate(), "../escape should be rejected")

	cfg.Services["api"] = Service{
		Exec:  []string{"/bin/true"},
		Masks: []Mask{{Path: "/tmp/outside", Size: "1G"}},
	}
	require.Error(t, cfg.Validate(), "absolute path not under any mount should be rejected")
}

func TestPackages_TopLevelAccepted(t *testing.T) {
	cfg := Config{
		Project:  Project{ID: "x", SandboxName: "x-sbx"},
		Packages: []string{"jq", "postgresql-client"},
	}
	require.NoError(t, cfg.Validate())
}

func TestCheckUnknownKeys_RejectsLegacyPortOffset(t *testing.T) {
	yamlText := []byte(`
project:
  id: x
  sandbox_name: x-sbx
  port_offset: 51000
`)
	err := CheckUnknownKeys(yamlText)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "port_offset")
}

func TestParse_SecretTag_AsSecretRef(t *testing.T) {
	const yamlSrc = `
project:
  id: x
  sandbox_name: x
services:
  api:
    exec: ["/bin/true"]
    env:
      GITHUB_TOKEN: !secret github_token
      PLAIN: hello
`
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &cfg))
	svc := cfg.Services["api"]
	require.NotNil(t, svc.Env["GITHUB_TOKEN"].Secret)
	assert.Equal(t, "github_token", svc.Env["GITHUB_TOKEN"].Secret.Name)
	assert.Equal(t, "", svc.Env["GITHUB_TOKEN"].Literal)
	assert.Equal(t, "hello", svc.Env["PLAIN"].Literal)
	assert.Nil(t, svc.Env["PLAIN"].Secret)
}

func TestParse_NetworkAllow(t *testing.T) {
	const yamlSrc = `
project:
  id: x
  sandbox_name: x
network:
  allow:
    - github.com
    - "*.npmjs.org"
`
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &cfg))
	assert.Equal(t, []string{"github.com", "*.npmjs.org"}, cfg.Network.Allow)
}

func TestEnvValue_TokenFor(t *testing.T) {
	assert.Equal(t, "__DEVM_SECRET_github_token__", TokenFor("github_token"))
	assert.Equal(t, "__DEVM_SECRET_x__", TokenFor("x"))
}

func TestEnvValue_Render_LiteralAndSecret(t *testing.T) {
	literal := EnvValue{Literal: "hello"}
	assert.Equal(t, "hello", literal.Render())

	secret := EnvValue{Secret: &SecretRef{Name: "foo"}}
	assert.Equal(t, "__DEVM_SECRET_foo__", secret.Render())
}

func TestEnvValue_IsSecret(t *testing.T) {
	assert.False(t, EnvValue{Literal: "val"}.IsSecret())
	assert.True(t, EnvValue{Secret: &SecretRef{Name: "x"}}.IsSecret())
}

func TestParse_TopLevel_SecretTag_AsSecretRef(t *testing.T) {
	// !secret can also appear in the top-level env: map.
	const yamlSrc = `
project:
  id: x
  sandbox_name: x
env:
  API_KEY: !secret my_api_key
  PLAIN: world
`
	var cfg Config
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &cfg))
	require.NotNil(t, cfg.Env["API_KEY"].Secret)
	assert.Equal(t, "my_api_key", cfg.Env["API_KEY"].Secret.Name)
	assert.Equal(t, "world", cfg.Env["PLAIN"].Literal)
}

func TestEnvValue_Render_SecretUsesTokenFor(t *testing.T) {
	// Render() for a SecretRef must produce the same string as TokenFor().
	name := "some_secret"
	ev := EnvValue{Secret: &SecretRef{Name: name}}
	assert.Equal(t, TokenFor(name), ev.Render())
}

// TestParse_SecretInOverrideFile exercises !secret decoding via
// the serviceOverrideYAML path (serviceOverride.Env is map[string]EnvValue).
func TestParse_SecretInServiceOverrideEnv(t *testing.T) {
	const yamlSrc = `
services:
  api:
    env:
      TOKEN: !secret my_token
`
	var override ConfigOverride
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &override))
	svc := override.Services["api"]
	require.NotNil(t, svc.Env["TOKEN"].Secret)
	assert.Equal(t, "my_token", svc.Env["TOKEN"].Secret.Name)
}

func TestWriteInTempDir_SecretTagPreservedThroughEnvFile(t *testing.T) {
	// Secret tokens render as the opaque __DEVM_SECRET_<name>__ form
	// in the devm.yaml temp-dir parse path. (Full Load path via
	// internal/config skips secrets in ResolveEnv, passing them through.)
	// This test covers the schema-level Render() contract only.
	tmp := t.TempDir()
	const yamlSrc = `
project:
  id: x
  sandbox_name: x
services:
  api:
    exec: ["/bin/true"]
    env:
      TOKEN: !secret gh_token
`
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "devm.yaml"), []byte(yamlSrc), 0o644))
	data, err := os.ReadFile(filepath.Join(tmp, "devm.yaml"))
	require.NoError(t, err)
	var cfg Config
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	token := cfg.Services["api"].Env["TOKEN"]
	require.NotNil(t, token.Secret)
	assert.Equal(t, "__DEVM_SECRET_gh_token__", token.Render())
}
