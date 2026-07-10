package devmbundle

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuild_ContainsExpectedFilesWithModes(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Env: map[string]schema.EnvValue{
			"FOO": {Literal: "bar"},
		},
	}
	body, err := Build(cfg, "/tmp/repo")
	require.NoError(t, err)

	entries := readTar(t, body)
	want := map[string]int64{
		".env":                        0o644,
		"scripts/with-devm-env":       0o755,
		"scripts/install-templates.sh": 0o755,
		"install.sh":                  0o755,
	}
	for path, mode := range want {
		e, ok := entries[path]
		require.True(t, ok, "bundle missing %s", path)
		assert.Equal(t, mode, e.mode&0o777, "%s mode", path)
		assert.Equal(t, int64(0), e.uid, "%s uid", path)
		assert.Equal(t, int64(0), e.gid, "%s gid", path)
	}
}

func TestBuild_EnvReflectsConfig(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Env: map[string]schema.EnvValue{
			"MYVAR": {Literal: "myval"},
		},
	}
	body, err := Build(cfg, "/tmp/repo")
	require.NoError(t, err)

	entries := readTar(t, body)
	envBody := string(entries[".env"].body)
	assert.Contains(t, envBody, "MYVAR")
	assert.Contains(t, envBody, "myval")
}

func TestBuild_Deterministic(t *testing.T) {
	// Two builds of the same cfg must produce byte-identical tars —
	// required so future callers can gate re-pipe on content hash
	// without spurious drift.
	cfg := schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}}
	a, err := Build(cfg, "/tmp/repo")
	require.NoError(t, err)
	b, err := Build(cfg, "/tmp/repo")
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

type tarEntry struct {
	mode int64
	uid  int64
	gid  int64
	body []byte
}

func readTar(t *testing.T, blob []byte) map[string]tarEntry {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(blob))
	out := map[string]tarEntry{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		out[hdr.Name] = tarEntry{mode: hdr.Mode, uid: int64(hdr.Uid), gid: int64(hdr.Gid), body: body}
	}
	return out
}
