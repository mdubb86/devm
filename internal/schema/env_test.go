package schema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveEnvInjectsWorkspaceAndIsSandbox(t *testing.T) {
	cfg := Config{}
	require.NoError(t, ResolveEnv(&cfg, "/Users/me/proj"))
	assert.Equal(t, "/Users/me/proj", cfg.Env["WORKSPACE"])
	assert.Equal(t, "1", cfg.Env["IS_SANDBOX"])
}

func TestResolveEnvExpandsWorkspaceInTopLevelValues(t *testing.T) {
	cfg := Config{Env: map[string]string{"CLAUDE_CONFIG_DIR": "$WORKSPACE/.claude"}}
	require.NoError(t, ResolveEnv(&cfg, "/Users/me/proj"))
	assert.Equal(t, "/Users/me/proj/.claude", cfg.Env["CLAUDE_CONFIG_DIR"])
}

func TestResolveEnvExpandsBraceForm(t *testing.T) {
	cfg := Config{Env: map[string]string{"X": "${WORKSPACE}/x"}}
	require.NoError(t, ResolveEnv(&cfg, "/r"))
	assert.Equal(t, "/r/x", cfg.Env["X"])
}

func TestResolveEnvExpandsInPerServiceEnv(t *testing.T) {
	cfg := Config{Services: map[string]Service{
		"caddy": {Port: 8080, Env: map[string]string{"ROOT": "$WORKSPACE/site"}},
	}}
	require.NoError(t, ResolveEnv(&cfg, "/r"))
	assert.Equal(t, "/r/site", cfg.Services["caddy"].Env["ROOT"])
}

func TestResolveEnvEscapeDoubleDollar(t *testing.T) {
	cfg := Config{Env: map[string]string{"X": "price $$5"}}
	require.NoError(t, ResolveEnv(&cfg, "/r"))
	assert.Equal(t, "price $5", cfg.Env["X"])
}

func TestResolveEnvErrorsOnUnknownVarTopLevel(t *testing.T) {
	cfg := Config{Env: map[string]string{"X": "$HOME/x"}}
	err := ResolveEnv(&cfg, "/r")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "env.X")
	assert.Contains(t, err.Error(), "$HOME")
}

func TestResolveEnvErrorsOnUnknownVarBraceForm(t *testing.T) {
	cfg := Config{Env: map[string]string{"X": "${HOME}/x"}}
	err := ResolveEnv(&cfg, "/r")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "$HOME")
}

func TestResolveEnvErrorsOnUnknownVarPerService(t *testing.T) {
	cfg := Config{Services: map[string]Service{
		"caddy": {Port: 8080, Env: map[string]string{"X": "$NOPE"}},
	}}
	err := ResolveEnv(&cfg, "/r")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "services.caddy.env.X")
	assert.Contains(t, err.Error(), "$NOPE")
}

func TestResolveEnvErrorsOnReservedKeyTopLevelWorkspace(t *testing.T) {
	cfg := Config{Env: map[string]string{"WORKSPACE": "/tmp/x"}}
	err := ResolveEnv(&cfg, "/r")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WORKSPACE")
	assert.Contains(t, err.Error(), "reserved")
}

func TestResolveEnvErrorsOnReservedKeyTopLevelIsSandbox(t *testing.T) {
	cfg := Config{Env: map[string]string{"IS_SANDBOX": "0"}}
	err := ResolveEnv(&cfg, "/r")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IS_SANDBOX")
	assert.Contains(t, err.Error(), "reserved")
}

func TestResolveEnvErrorsOnReservedKeyPerService(t *testing.T) {
	cfg := Config{Services: map[string]Service{
		"caddy": {Port: 8080, Env: map[string]string{"WORKSPACE": "/tmp/x"}},
	}}
	err := ResolveEnv(&cfg, "/r")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "services.caddy.env.WORKSPACE")
	assert.Contains(t, err.Error(), "reserved")
}

func TestResolveEnvNoSideEffectsOnError(t *testing.T) {
	cfg := Config{Env: map[string]string{"X": "$HOME"}}
	_ = ResolveEnv(&cfg, "/r")
	// On error, no injection should have happened.
	_, hasWS := cfg.Env["WORKSPACE"]
	_, hasSB := cfg.Env["IS_SANDBOX"]
	assert.False(t, hasWS, "WORKSPACE should not be injected on error")
	assert.False(t, hasSB, "IS_SANDBOX should not be injected on error")
}

func TestResolveEnvNilCfgEnvGetsPopulated(t *testing.T) {
	cfg := Config{Env: nil}
	require.NoError(t, ResolveEnv(&cfg, "/r"))
	require.NotNil(t, cfg.Env)
	assert.Equal(t, "/r", cfg.Env["WORKSPACE"])
}

func TestResolveEnvErrorMentionsFileLine_TBD(t *testing.T) {
	// File:line context is captured at YAML unmarshal time via yaml.Node.
	// Today's schema decoders don't preserve line info for env map values,
	// so error messages currently only name the field path (env.X /
	// services.NAME.env.X). Pinning future-improvement here so we don't
	// silently regress when line info becomes available.
	cfg := Config{Env: map[string]string{"X": "$NOPE"}}
	err := ResolveEnv(&cfg, "/r")
	require.Error(t, err)
	// Today: contains field path. Future: also contains "devm.yaml:NN".
	assert.True(t, strings.Contains(err.Error(), "env.X"))
}
