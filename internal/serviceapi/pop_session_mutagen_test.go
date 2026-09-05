package serviceapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPopSessionSyncConfig_File(t *testing.T) {
	cfg := popSessionSyncConfig(PopKindFile, "index.html")
	assert.Equal(t, "one-way-safe", cfg.SyncMode)
	assert.Equal(t, "accelerated", cfg.ScanMode)
	assert.False(t, cfg.VCSIgnore)
	assert.Equal(t, []string{"**", "!index.html"}, cfg.Ignores)
}

func TestPopSessionSyncConfig_Dir(t *testing.T) {
	cfg := popSessionSyncConfig(PopKindDir, "")
	assert.Equal(t, "one-way-safe", cfg.SyncMode)
	assert.Equal(t, "accelerated", cfg.ScanMode)
	assert.False(t, cfg.VCSIgnore)
	assert.Empty(t, cfg.Ignores)
}

func TestCreatePopSyncSession_FileKind_WritesConfigAndCallsSyncCreate(t *testing.T) {
	tmp := t.TempDir()
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()

	ps := &PopSession{
		ID: "sid1", ProjectName: "proj", GuestPath: "/tmp/site/index.html",
		Kind: PopKindFile, MacDir: filepath.Join(tmp, "pop-tmp", "sid1"),
		TargetName: "index.html",
	}
	require.NoError(t, CreatePopSyncSession(cli, cfg, "devm-proj", ps))

	// Config file present with expected ignores + one-way-safe mode.
	body, err := os.ReadFile(popSessionConfigPath(cfg, "proj", "sid1"))
	require.NoError(t, err)
	s := string(body)
	assert.Contains(t, s, "mode: one-way-safe")
	assert.Contains(t, s, "scanMode: accelerated")
	assert.Contains(t, s, "- \"**\"")
	assert.Contains(t, s, "- \"!index.html\"")

	// Mac dir was created.
	info, err := os.Stat(ps.MacDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// sync create call used the file's PARENT dir as alpha.
	require.NotEmpty(t, scripted.createArgs)
	lastCreate := scripted.createArgs[len(scripted.createArgs)-1]
	assert.Equal(t, "sync", lastCreate[0])
	assert.Equal(t, "create", lastCreate[1])
	assert.Contains(t, lastCreate, "devm@devm-proj:/tmp/site")
	assert.NotContains(t, lastCreate, "devm@devm-proj:/tmp/site/index.html")
	assert.Contains(t, lastCreate, ps.MacDir)

	assert.Equal(t, "sess-new", ps.MutagenSessionID)
}

func TestCreatePopSyncSession_DirKind_UsesGuestPathAsAlpha(t *testing.T) {
	tmp := t.TempDir()
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()

	ps := &PopSession{
		ID: "did1", ProjectName: "proj", GuestPath: "/tmp/site",
		Kind: PopKindDir, MacDir: filepath.Join(tmp, "pop-tmp", "did1"),
	}
	require.NoError(t, CreatePopSyncSession(cli, cfg, "devm-proj", ps))
	lastCreate := scripted.createArgs[len(scripted.createArgs)-1]
	assert.Contains(t, lastCreate, "devm@devm-proj:/tmp/site",
		"dir kind uses the guest path itself as alpha")
}

func TestTearDownPopSyncSession_RemovesMacDirAndConfig(t *testing.T) {
	tmp := t.TempDir()
	cfg := testPopSessionCfg(t)

	scripted := &scriptedCLI{}
	cli := scripted.build()

	ps := &PopSession{
		ID: "tid1", ProjectName: "proj", GuestPath: "/tmp/z", Kind: PopKindDir,
		MacDir: filepath.Join(tmp, "pop-tmp", "tid1"),
	}
	require.NoError(t, CreatePopSyncSession(cli, cfg, "devm-proj", ps))

	// Sanity: files exist first.
	_, err := os.Stat(ps.MacDir)
	require.NoError(t, err)
	_, err = os.Stat(popSessionConfigPath(cfg, "proj", "tid1"))
	require.NoError(t, err)

	require.NoError(t, TearDownPopSyncSession(cli, cfg, *ps))
	_, err = os.Stat(ps.MacDir)
	assert.True(t, os.IsNotExist(err), "MacDir removed")
	_, err = os.Stat(popSessionConfigPath(cfg, "proj", "tid1"))
	assert.True(t, os.IsNotExist(err), "config file removed")

	// sync terminate was invoked with this session's mutagen id.
	require.NotEmpty(t, scripted.terminateCall)
	assert.Equal(t, ps.MutagenSessionID, scripted.terminateCall[len(scripted.terminateCall)-1])
}
