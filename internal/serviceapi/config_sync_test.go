package serviceapi

import (
	"context"
	"os"
	"testing"

	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigSyncSessionName_Format(t *testing.T) {
	assert.Equal(t, "devm-config-myproj", ConfigSyncSessionName("myproj"))
}

func TestSetupConfigSync_CreatesSessionWithScopedFilter(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{} // no existing sessions
	cli := sc.build()

	macCwd := t.TempDir()
	err := SetupConfigSync(context.Background(), cli, cfg, "myproj", macCwd)
	require.NoError(t, err)

	require.Len(t, sc.createArgs, 1)
	args := sc.createArgs[0]
	assert.Contains(t, args, "--name")
	assert.Contains(t, args, "devm-config-myproj")
	assert.Contains(t, args, macCwd)
	assert.Contains(t, args, "devm@devm-myproj:/home/devm")

	// The generated config file must scope the include filter to
	// exactly devm.yaml and devm.me.yaml — nothing else in <macCwd>
	// may cross.
	configPath := mutagen.ConfigFilePath(mutagenSessionsDir(cfg), "myproj", configSyncLabel)
	body, err := os.ReadFile(configPath)
	require.NoError(t, err)
	content := string(body)
	assert.Contains(t, content, `"*"`, "blanket ignore must be present")
	assert.Contains(t, content, `"!devm.yaml"`, "devm.yaml must be un-ignored")
	assert.Contains(t, content, `"!devm.me.yaml"`, "devm.me.yaml must be un-ignored")
}

func TestSetupConfigSync_AlphaIsMacCwd(t *testing.T) {
	cli := &scriptedCLI{}
	cfg := testSessionsIdentity(t)
	macCwd := t.TempDir()
	err := SetupConfigSync(context.Background(), cli.build(), cfg, "p", macCwd)
	require.NoError(t, err)
	require.Len(t, cli.createArgs, 1)
	assert.Contains(t, cli.createArgs[0], macCwd,
		"alpha path must be the project's Mac cwd, not the state-dir")
}

func TestSetupConfigSync_EmptyMacCwdIsError(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{}
	cli := sc.build()

	err := SetupConfigSync(context.Background(), cli, cfg, "myproj", "")
	require.Error(t, err)
	assert.Empty(t, sc.createArgs, "must not create a session with an empty alpha")
}

func TestSetupConfigSync_ExistingSessionIsNoop(t *testing.T) {
	cfg := testSessionsIdentity(t)
	sc := &scriptedCLI{
		listSessions: []mutagen.SyncSession{
			{ID: "sess-1", Name: "devm-config-myproj", Status: "Watching", Paused: false},
		},
	}
	cli := sc.build()

	err := SetupConfigSync(context.Background(), cli, cfg, "myproj", t.TempDir())
	require.NoError(t, err)

	assert.Empty(t, sc.createArgs, "warm attach must not recreate an existing config-sync session")
}

func TestStopConfigSync_TerminatesByName(t *testing.T) {
	sc := &scriptedCLI{
		listSessions: []mutagen.SyncSession{
			{ID: "sess-1", Name: "devm-config-myproj", Status: "Watching", Paused: false},
		},
	}
	cli := sc.build()

	err := StopConfigSync(context.Background(), cli, "myproj")
	require.NoError(t, err)

	assert.Equal(t, []string{"sess-1"}, sc.terminateCall)
}

func TestStopConfigSync_NoSessionIsNoop(t *testing.T) {
	sc := &scriptedCLI{} // no existing sessions
	cli := sc.build()

	err := StopConfigSync(context.Background(), cli, "myproj")
	require.NoError(t, err)

	assert.Empty(t, sc.terminateCall)
}
