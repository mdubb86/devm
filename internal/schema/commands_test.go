package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func p[T any](v T) *T { return &v }

func TestRepoConfig_ValidateCommands_NameShape(t *testing.T) {
	cases := []struct {
		name    string
		cmds    []string
		wantErr string
	}{
		{"lowercase-and-digits ok", []string{"install2"}, ""},
		{"kebab ok", []string{"fmt-check"}, ""},
		{"starts with digit", []string{"1install"}, "function name"},
		{"uppercase", []string{"Install"}, "function name"},
		{"underscore rejected", []string{"fmt_check"}, "function name"},
		{"empty name", []string{""}, "function name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := RepoConfig{Commands: c.cmds}
			err := r.validateCommands()
			if c.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.wantErr)
		})
	}
}

func TestRepoConfig_ValidateCommands_EmptyIsValid(t *testing.T) {
	assert.NoError(t, RepoConfig{}.validateCommands())
}
