package sandbox

import (
	"os"
	"strings"
	"testing"

	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
)

// ---- EnvArgs (post-split): forwards host vars ONLY ----

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

func TestEnvArgsDoesNotEmitCfgEnv(t *testing.T) {
	cfg := schema.Config{Env: map[string]string{"FOO": "bar"}}
	args := EnvArgs(cfg)
	for _, a := range args {
		assert.NotContains(t, a, "FOO=", "cfg.Env must NOT appear in EnvArgs; it goes in PersistentEnv")
	}
}

func TestEnvArgsDoesNotEmitServiceEnvOrInjectedPorts(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{PortOffset: 10},
		Services: map[string]schema.Service{
			"db": {Port: 5432, EnvInject: true, EnvHost: "0.0.0.0",
				Env: map[string]string{"KEY": "val"}},
		},
	}
	args := EnvArgs(cfg)
	for _, a := range args {
		assert.NotContains(t, a, "DB_PORT=", "moved to PersistentEnv")
		assert.NotContains(t, a, "DB_HOST=", "moved to PersistentEnv")
		assert.NotContains(t, a, "DB_KEY=", "moved to PersistentEnv")
	}
}

// ---- PersistentEnv: file contents ----

func TestPersistentEnvEmptyConfigStillHasPathLine(t *testing.T) {
	cfg := schema.Config{Project: schema.Project{PortOffset: 0}}
	out := PersistentEnv(cfg)
	assert.Contains(t, out, `export PATH="$WORKSPACE/.devm/scripts:$PATH"`)
	// PATH line must be last so $WORKSPACE has been exported by an earlier line.
	assert.True(t, strings.HasSuffix(strings.TrimRight(out, "\n"), `export PATH="$WORKSPACE/.devm/scripts:$PATH"`))
}

func TestPersistentEnvExportsCfgEnvSorted(t *testing.T) {
	cfg := schema.Config{Env: map[string]string{
		"BBB": "two",
		"AAA": "one",
	}}
	out := PersistentEnv(cfg)
	aIdx := strings.Index(out, "export AAA=")
	bIdx := strings.Index(out, "export BBB=")
	assert.Greater(t, aIdx, -1)
	assert.Greater(t, bIdx, aIdx, "keys must be sorted")
}

func TestPersistentEnvSingleQuotesValues(t *testing.T) {
	cfg := schema.Config{Env: map[string]string{
		"PATHY":  "/has spaces/and$dollar",
		"QUOTED": "it's mine",
	}}
	out := PersistentEnv(cfg)
	assert.Contains(t, out, `export PATHY='/has spaces/and$dollar'`)
	assert.Contains(t, out, `export QUOTED='it'\''s mine'`)
}

func TestPersistentEnvServiceEnvFlatPrefixed(t *testing.T) {
	cfg := schema.Config{Services: map[string]schema.Service{
		"caddy": {Port: 8080, Env: map[string]string{"ROOT": "/srv"}},
	}}
	out := PersistentEnv(cfg)
	assert.Contains(t, out, `export CADDY_ROOT='/srv'`)
}

func TestPersistentEnvEnvInjectEmitsPortAndHost(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{PortOffset: 10},
		Services: map[string]schema.Service{
			"app": {Port: 3000, EnvInject: true, EnvHost: "0.0.0.0"},
		},
	}
	out := PersistentEnv(cfg)
	assert.Contains(t, out, `export APP_PORT='3010'`)
	assert.Contains(t, out, `export APP_HOST='0.0.0.0'`)
}

func TestPersistentEnvEnvInjectOmitsHostWhenNotSet(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{PortOffset: 10},
		Services: map[string]schema.Service{
			"app": {Port: 3000, EnvInject: true},
		},
	}
	out := PersistentEnv(cfg)
	assert.Contains(t, out, `export APP_PORT='3010'`)
	assert.NotContains(t, out, "APP_HOST=", "omit when not set")
}

func TestPersistentEnvSkipsSupabasePrefixForPortInject(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{PortOffset: 10},
		Services: map[string]schema.Service{
			"supabase_api": {Port: 54321, EnvInject: true},
		},
	}
	out := PersistentEnv(cfg)
	assert.NotContains(t, out, "SUPABASE_API_PORT=", "supabase-prefix services must skip port injection")
}

func TestPersistentEnvDeterministicAcrossRuns(t *testing.T) {
	cfg := schema.Config{
		Env: map[string]string{"A": "1", "B": "2", "C": "3"},
		Services: map[string]schema.Service{
			"x": {Port: 1000, EnvInject: true, Env: map[string]string{"K": "v"}},
			"y": {Port: 2000, EnvInject: true},
		},
	}
	a := PersistentEnv(cfg)
	b := PersistentEnv(cfg)
	assert.Equal(t, a, b, "must produce byte-identical output across calls")
}
