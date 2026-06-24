package schema

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

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
	// Minimum valid: just canonical
	s := Service{Port: 3000}
	assert.NoError(t, s.Validate())

	// env_host without env_inject is a misconfiguration
	bad := Service{Port: 3000, EnvHost: "0.0.0.0"}
	assert.Error(t, bad.Validate(), "env_host requires env_inject=true")

	// env_inject without canonical port has nothing to inject
	noPort := Service{EnvInject: true}
	assert.Error(t, noPort.Validate(), "env_inject requires canonical")

	// Workspace pseudo-service: no port OK, but must have at least one mask
	workspace := Service{Masks: []Mask{{Path: "node_modules", Size: "2G"}}}
	assert.NoError(t, workspace.Validate())

	emptyWorkspace := Service{}
	assert.Error(t, emptyWorkspace.Validate(), "service must have canonical or at least one mask")
}

func TestConfigValidate(t *testing.T) {
	c := Config{
		Project: Project{
			ID:          "test",
			SandboxName: "test-sbx",
		},
		BaseImage: BaseImage{Docker: true},
		Network:   Network{AllowedDomains: []string{"github.com"}},
		Services: map[string]Service{
			"webapp": {Port: 3000, Hostname: "test.local"},
		},
	}
	assert.NoError(t, c.Validate())

	// Hostname collision across services
	dup := c
	dup.Services = map[string]Service{
		"webapp": {Port: 3000, Hostname: "test.local"},
		"api":    {Port: 8080, Hostname: "test.local"},
	}
	assert.Error(t, dup.Validate(), "duplicate hostname")

	// Port collision across services
	dup2 := Config{
		Project:   c.Project,
		BaseImage: c.BaseImage,
		Network:   c.Network,
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

	// port_offset + canonical exceeds 65535 → error.
	over := base
	over.Project.PortOffset = 70000
	over.Services = map[string]Service{"api": {Port: 8080}}
	err := over.Validate()
	require.Error(t, err, "offset+canonical over 65535 must error")
	assert.Contains(t, err.Error(), "65535")

	// canonical out of range (negative / too large) → error.
	bigPort := base
	bigPort.Services = map[string]Service{"api": {Port: 70000}}
	assert.Error(t, bigPort.Validate(), "canonical over 65535 must error")

	// Valid combination → no error.
	ok := base
	ok.Project.PortOffset = 51000
	ok.Services = map[string]Service{"api": {Port: 8080}} // 59080, fine
	assert.NoError(t, ok.Validate())

	// Exactly at the boundary is allowed.
	boundary := base
	boundary.Project.PortOffset = 60000
	boundary.Services = map[string]Service{"api": {Port: 5535}} // 65535
	assert.NoError(t, boundary.Validate())
}

func TestStartupCommandRequiresNonEmptyCommand(t *testing.T) {
	err := StartupCommand{}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command")

	err = StartupCommand{Command: []string{}}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command")

	err = StartupCommand{Command: []string{""}}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command")

	err = StartupCommand{Command: []string{"pg_ctl", "start"}}.Validate()
	assert.NoError(t, err)
}

func TestConfigValidatesInstallSteps(t *testing.T) {
	cfg := Config{
		Project:   Project{ID: "x", SandboxName: "x-sbx"},
		BaseImage: BaseImage{Docker: false},
		Install: []string{
			"", // invalid
		},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install[0]")
}

func TestServiceValidatesStartupSteps(t *testing.T) {
	svc := Service{
		Startup: []StartupCommand{
			{Command: []string{}}, // invalid
		},
	}
	err := svc.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "startup[0]")
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

func TestServiceMayHaveOnlyStartup(t *testing.T) {
	// A daemon-style service: no canonical port, no masks, but startup.
	svc := Service{
		Startup: []StartupCommand{
			{Command: []string{"my-daemon"}},
		},
	}
	err := svc.Validate()
	assert.NoError(t, err, "a service with only startup commands should be valid")
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
