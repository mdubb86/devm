package devmbundle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildInputFor_PopulatesGdevmAndCommandsManifest(t *testing.T) {
	cfg := schema.Config{Project: schema.Project{Name: "p"}}

	in, err := BuildInputFor(
		cfg, "/tmp/repo", "", "/tmp/rt", "e2e.test",
		[]byte("ca"), []byte("ssh-pub"), []byte("ssh-priv"), []byte("host-pub"),
	)
	require.NoError(t, err)

	assert.Equal(t, cfg, in.Cfg)
	assert.Equal(t, "/tmp/repo", in.RepoRoot)
	assert.Equal(t, "/tmp/rt", in.DaemonRuntimeDir)
	assert.Equal(t, "e2e.test", in.TLD)
	assert.Equal(t, []byte("ca"), in.CARootPEM)
	assert.Equal(t, []byte("ssh-pub"), in.SSHAuthorizedPubkey)
	assert.NotEmpty(t, in.Gdevm, "must include gdevm bytes")
	assert.NotEmpty(t, in.CommandsManifest, "must render commands manifest even with empty repos")
	assert.NotEmpty(t, in.MutagenAgentLinuxArm64, "must include mutagen agent")
	assert.NotEmpty(t, in.MutagenVersion, "must set mutagen version")
	assert.Empty(t, in.DockerRuncShim, "docker: false → no runc shim")
}

func TestBuildInputFor_IncludesDockerShimsWhenEnabled(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Docker:  &schema.DockerConfig{Enabled: true},
	}

	in, err := BuildInputFor(
		cfg, "/tmp/repo", "", "/tmp/rt", "e2e.test",
		nil, nil, nil, nil,
	)
	require.NoError(t, err)

	assert.NotEmpty(t, in.DockerRuncShim, "docker enabled → runc shim populated")
	assert.NotEmpty(t, in.DockerCLIShim, "docker enabled → cli shim populated")
}

func TestBuildInputFor_FailsLoudlyWhenSSHConfiguredButEnricherUnbound(t *testing.T) {
	// SSHEnricher is a package var bound by RunService at daemon start.
	// Save and restore so other tests aren't affected.
	prev := SSHEnricher
	SSHEnricher = nil
	t.Cleanup(func() { SSHEnricher = prev })

	ssh := []string{"host1"}
	cfg := schema.Config{
		Project: schema.Project{
			Name: "p",
		},
		SSH: &ssh,
	}

	_, err := BuildInputFor(
		cfg, "/tmp/repo", "", "/tmp/rt", "e2e.test",
		nil, nil, nil, nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSHEnricher not bound",
		"missing enricher must be a loud failure, not a silent empty bundle")
}

// TestBuildInputFor_ReadsDevmSh proves BuildInputFor reads devm.sh
// and devm.me.sh from macCwd into the DevmSh / DevmMeSh BuildInput
// fields so bundle.Build can ship them to /opt/devm/ — where
// tartHookRunner sources them before every backup_<name> /
// restore_<name> invocation.
func TestBuildInputFor_ReadsDevmSh(t *testing.T) {
	macCwd := t.TempDir()
	shBody := []byte("backup_appstate() { true; }\n")
	meBody := []byte("backup_appstate() { /usr/local/bin/me-backup; }\n")
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.sh"), shBody, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(macCwd, "devm.me.sh"), meBody, 0o644))

	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	in, err := BuildInputFor(
		cfg, "/tmp/repo", macCwd, "/tmp/rt", "e2e.test",
		nil, nil, nil, nil,
	)
	require.NoError(t, err)

	assert.Equal(t, shBody, in.DevmSh, "DevmSh must carry devm.sh bytes verbatim")
	assert.Equal(t, meBody, in.DevmMeSh, "DevmMeSh must carry devm.me.sh bytes verbatim")
}

// TestBuildInputFor_MissingDevmShIsEmpty proves a macCwd without
// devm.sh / devm.me.sh produces empty fields with no error — these
// files are optional, and a project without hooks still bundles.
func TestBuildInputFor_MissingDevmShIsEmpty(t *testing.T) {
	macCwd := t.TempDir() // empty dir, no devm.sh or devm.me.sh

	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	in, err := BuildInputFor(
		cfg, "/tmp/repo", macCwd, "/tmp/rt", "e2e.test",
		nil, nil, nil, nil,
	)
	require.NoError(t, err)

	assert.Empty(t, in.DevmSh, "missing devm.sh must leave DevmSh empty, not error")
	assert.Empty(t, in.DevmMeSh, "missing devm.me.sh must leave DevmMeSh empty, not error")
}
