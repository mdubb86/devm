package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mtwaage/devm/internal/sandbox"
	"github.com/mtwaage/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteDevmEnvWritesPersistentEnvToDotenv(t *testing.T) {
	dir := t.TempDir()
	cfg := schema.Config{Env: map[string]string{"X": "y"}}

	require.NoError(t, WriteDevmEnv(cfg, dir))

	got, err := os.ReadFile(filepath.Join(dir, ".devm", ".env"))
	require.NoError(t, err)
	assert.Equal(t, sandbox.PersistentEnv(cfg), string(got))
}

func TestWriteDevmEnvCreatesDotDevmDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteDevmEnv(schema.Config{}, dir))

	info, err := os.Stat(filepath.Join(dir, ".devm"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestWriteDevmEnvMode0644(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteDevmEnv(schema.Config{}, dir))

	info, err := os.Stat(filepath.Join(dir, ".devm", ".env"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestWriteDevmEnvOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".devm", ".env")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".devm"), 0o755))
	require.NoError(t, os.WriteFile(envPath, []byte("# stale\n"), 0o644))

	cfg := schema.Config{Env: map[string]string{"NEW": "val"}}
	require.NoError(t, WriteDevmEnv(cfg, dir))

	got, err := os.ReadFile(envPath)
	require.NoError(t, err)
	assert.NotContains(t, string(got), "# stale")
	assert.Contains(t, string(got), "export NEW='val'")
}

func TestWriteDevmEnvIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := schema.Config{Env: map[string]string{"A": "1"}}
	require.NoError(t, WriteDevmEnv(cfg, dir))
	first, err := os.ReadFile(filepath.Join(dir, ".devm", ".env"))
	require.NoError(t, err)
	require.NoError(t, WriteDevmEnv(cfg, dir))
	second, err := os.ReadFile(filepath.Join(dir, ".devm", ".env"))
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

func TestWriteDevmEnvLeavesNoTmpfileBehind(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteDevmEnv(schema.Config{}, dir))
	entries, err := os.ReadDir(filepath.Join(dir, ".devm"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, e.Name() != ".env" && filepath.Ext(e.Name()) == ".tmp",
			"tmpfile should not survive a successful write")
	}
}
