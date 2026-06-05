package sandbox

import (
	"os"
	"testing"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestEnvArgsForwardsHostTermVars(t *testing.T) {
	os.Setenv("TERM", "xterm-ghostty")
	os.Setenv("COLORTERM", "truecolor")
	defer os.Unsetenv("TERM")
	defer os.Unsetenv("COLORTERM")

	cfg := schema.Config{Project: schema.Project{PortOffset: 10}}
	args := EnvArgs(cfg)
	assert.Contains(t, args, "-e")
	assert.Contains(t, args, "TERM=xterm-ghostty")
	assert.Contains(t, args, "COLORTERM=truecolor")
}

func TestEnvArgsInjectsServicePorts(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{PortOffset: 10},
		Services: map[string]schema.Service{
			"brainstorm": {
				Port: 52345,
				EnvInject: true,
				EnvHost:   "0.0.0.0",
			},
		},
	}
	args := EnvArgs(cfg)
	assert.Contains(t, args, "BRAINSTORM_PORT=52355")
	assert.Contains(t, args, "BRAINSTORM_HOST=0.0.0.0")
}

func TestEnvArgsRejectsSupabasePrefix(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{PortOffset: 10},
		Services: map[string]schema.Service{
			"supabase_api": {
				Port: 54321,
				EnvInject: true,
			},
		},
	}
	args := EnvArgs(cfg)
	for _, a := range args {
		assert.NotContains(t, a, "SUPABASE_API_PORT=", "must not inject SUPABASE_*_PORT (collides with supabase CLI env-prefix)")
	}
}
