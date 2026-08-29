package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func p[T any](v T) *T { return &v }

func TestRepoCommand_Validate(t *testing.T) {
	scripts := map[string][]string{
		"fmt-check": {"echo fmt"},
		"empty":     {},
	}

	cases := []struct {
		name    string
		cmd     RepoCommand
		wantErr string
	}{
		{"literal exec ok", RepoCommand{Exec: "pnpm install"}, ""},
		{"script-ref ok", RepoCommand{Exec: ">fmt-check"}, ""},
		{"empty exec", RepoCommand{Exec: ""}, "exec is required"},
		{"ref to missing script", RepoCommand{Exec: ">missing"}, `references script "missing"`},
		{"ref to empty script", RepoCommand{Exec: ">empty"}, "empty script"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cmd.Validate(scripts)
			if c.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.wantErr)
		})
	}
}

func TestRepoConfig_ValidateCommands_NameShapeAndDupes(t *testing.T) {
	cases := []struct {
		name    string
		cmds    map[string]RepoCommand
		wantErr string
	}{
		{"lowercase-and-digits ok", map[string]RepoCommand{"install2": {Exec: "true"}}, ""},
		{"kebab ok", map[string]RepoCommand{"fmt-check": {Exec: "true"}}, ""},
		{"underscore ok", map[string]RepoCommand{"fmt_check": {Exec: "true"}}, ""},
		{"starts with digit", map[string]RepoCommand{"1install": {Exec: "true"}}, "command name"},
		{"uppercase", map[string]RepoCommand{"Install": {Exec: "true"}}, "command name"},
		{"empty name", map[string]RepoCommand{"": {Exec: "true"}}, "command name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := RepoConfig{Commands: c.cmds}
			err := r.validateCommands(nil)
			if c.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.wantErr)
		})
	}
}

func TestRepoCommand_UnmarshalYAML_UnknownKeyRejected(t *testing.T) {
	src := []byte(`
exec: pnpm test
run_on_setup: true
`)
	var cmd RepoCommand
	err := yamlUnmarshal(src, &cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run_on_setup")
	assert.Contains(t, err.Error(), "unknown")
}

func yamlUnmarshal(b []byte, v any) error {
	return yaml.Unmarshal(b, v)
}
