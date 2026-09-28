package devmbundle

import (
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildInputFor_PopulatesGdevmAndCommandsManifest(t *testing.T) {
	cfg := schema.Config{Project: schema.Project{Name: "p"}}

	in, err := BuildInputFor(
		cfg, "/tmp/repo", "/tmp/rt",
		[]byte("ca"), []byte("ssh-pub"), []byte("ssh-priv"), []byte("host-pub"),
	)
	require.NoError(t, err)

	assert.Equal(t, cfg, in.Cfg)
	assert.Equal(t, "/tmp/repo", in.RepoRoot)
	assert.Equal(t, "/tmp/rt", in.DaemonRuntimeDir)
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
		Docker:  true,
	}

	in, err := BuildInputFor(
		cfg, "/tmp/repo", "/tmp/rt",
		nil, nil, nil, nil,
	)
	require.NoError(t, err)

	assert.NotEmpty(t, in.DockerRuncShim, "docker: true → runc shim populated")
	assert.NotEmpty(t, in.DockerCLIShim, "docker: true → cli shim populated")
}
