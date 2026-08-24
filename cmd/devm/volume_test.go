package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunVolumeLs_EmptyVolumes_PrintsHeaderOnly(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ident := identity.Config{Name: "devm-test"}
	userCfg := schema.Config{Project: schema.Project{Name: "p"}}
	var buf bytes.Buffer
	require.NoError(t, runVolumeLs(ident, userCfg, &buf))
	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "GUEST PATH")
	assert.Contains(t, out, "MAC PATH")
	assert.Contains(t, out, "SIZE")
	// Only header — one line, no volume rows.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Len(t, lines, 1, "no volumes → header only")
}

func TestRunVolumeLs_ListsDeclaredVolumes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Seed the Mac-side volume dir with some content so SIZE is non-zero.
	ident := identity.Config{Name: "devm-test"}
	macDir := filepath.Join(tmp, "Library", "Application Support", "devm-test", "volumes", "p", "pg-data")
	require.NoError(t, os.MkdirAll(macDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(macDir, "PG_VERSION"), []byte("14"), 0644))

	userCfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Volumes: map[string]schema.Volume{
			"pg-data":      {Path: "/var/lib/postgresql/data"},
			"claude-cache": {Path: "/home/devm/.cache/claude"},
		},
	}
	var buf bytes.Buffer
	require.NoError(t, runVolumeLs(ident, userCfg, &buf))
	out := buf.String()
	assert.Contains(t, out, "pg-data")
	assert.Contains(t, out, "/var/lib/postgresql/data")
	assert.Contains(t, out, "claude-cache")
	assert.Contains(t, out, "/home/devm/.cache/claude")
	// Sizes appear in units — B/K/M/G. pg-data's PG_VERSION file is
	// 2 bytes; claude-cache doesn't exist as a dir yet (auto-treated
	// as 0B).
	assert.Regexp(t, `pg-data.+2\s?B`, out)
	assert.Regexp(t, `claude-cache.+0\s?B`, out)
}

func TestRunVolumeLs_SortedByName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ident := identity.Config{Name: "devm-test"}
	userCfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Volumes: map[string]schema.Volume{
			"zebra":  {Path: "/z"},
			"alpha":  {Path: "/a"},
			"middle": {Path: "/m"},
		},
	}
	var buf bytes.Buffer
	require.NoError(t, runVolumeLs(ident, userCfg, &buf))
	out := buf.String()
	// Rows sorted by name: alpha < middle < zebra.
	iA := strings.Index(out, "alpha")
	iM := strings.Index(out, "middle")
	iZ := strings.Index(out, "zebra")
	assert.Less(t, iA, iM)
	assert.Less(t, iM, iZ)
}
