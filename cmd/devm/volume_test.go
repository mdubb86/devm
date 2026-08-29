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

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

func TestRunVolumeLs_EmptyVolumes_PrintsHeaderOnly(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ident := identity.Config{Name: "devm-test"}
	userCfg := schema.Config{Project: schema.Project{Name: "p"}}
	var buf bytes.Buffer
	require.NoError(t, runVolumeLs(ident, userCfg, "/Users/me/projects/myproj", &buf))
	out := buf.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "LABEL")
	assert.Contains(t, out, "KIND")
	assert.Contains(t, out, "GUEST PATH")
	assert.Contains(t, out, "MAC PATH")
	assert.Contains(t, out, "SIZE")
	// Only header — one line, no rows.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Len(t, lines, 1, "no repos/volumes → header only")
}

func TestRunVolumeLs_VolumeOnly_ListsDeclaredVolumes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Seed the Mac-side mirror dir with some content so SIZE is non-zero.
	ident := identity.Config{Name: "devm-test"}
	macDir := filepath.Join(tmp, "Library", "Application Support", "devm-test", "p", "pg-data")
	require.NoError(t, os.MkdirAll(macDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(macDir, "PG_VERSION"), []byte("14"), 0644))

	userCfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Volumes: map[string]schema.Volume{
			// Path's leaf dir is the resolved label absent an explicit
			// `label:`, so name it to match for a legible test.
			"pg-data":      {Path: "/var/lib/postgresql/pg-data"},
			"claude-cache": {Path: "/home/devm/.cache/claude-cache"},
		},
	}
	var buf bytes.Buffer
	require.NoError(t, runVolumeLs(ident, userCfg, "", &buf))
	out := buf.String()
	assert.Contains(t, out, "pg-data")
	assert.Contains(t, out, "volume")
	assert.Contains(t, out, "/var/lib/postgresql/pg-data")
	assert.Contains(t, out, "claude-cache")
	assert.Contains(t, out, "/home/devm/.cache/claude-cache")
	// Mac path uses <runtimeDir>/<project>/<label> — no "volumes" segment.
	assert.Contains(t, out, filepath.Join(tmp, "Library", "Application Support", "devm-test", "p", "pg-data"))
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
	require.NoError(t, runVolumeLs(ident, userCfg, "", &buf))
	out := buf.String()
	// Rows sorted by name: alpha < middle < zebra.
	iA := strings.Index(out, "alpha")
	iM := strings.Index(out, "middle")
	iZ := strings.Index(out, "zebra")
	assert.Less(t, iA, iM)
	assert.Less(t, iM, iZ)
}

func TestRunVolumeLs_ReposBeforeVolumes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ident := identity.Config{Name: "devm-test"}
	userCfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"zzz-repo": {Primary: boolPtr(true)},
		},
		Volumes: map[string]schema.Volume{
			"aaa-vol": {Path: "/a"},
		},
	}
	var buf bytes.Buffer
	require.NoError(t, runVolumeLs(ident, userCfg, "/Users/me/projects/zzz-repo", &buf))
	out := buf.String()
	// Repos sort before volumes regardless of name ordering.
	assert.Less(t, strings.Index(out, "zzz-repo"), strings.Index(out, "aaa-vol"))
}

func TestRunVolumeLs_PrimaryRepoWithURL_HasMacPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ident := identity.Config{Name: "devm-test"}
	url := "git@github.com:acme/myapp.git"
	userCfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"app": {URL: strPtr(url), Primary: boolPtr(true)},
		},
	}
	var buf bytes.Buffer
	require.NoError(t, runVolumeLs(ident, userCfg, "/Users/me/projects/myapp", &buf))
	out := buf.String()
	assert.Contains(t, out, "app")
	assert.Contains(t, out, "myapp") // label: BareCloneName(url)
	assert.Contains(t, out, "repo")
	assert.Contains(t, out, "/home/devm/myapp")
	assert.Contains(t, out, filepath.Join(tmp, "Library", "Application Support", "devm-test", "p", "myapp"))
}

func TestRunVolumeLs_PrimaryRepoURLNil_LabelFromCwdBasename(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ident := identity.Config{Name: "devm-test"}
	userCfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"app": {}, // URL nil, sole entry → implicit primary
		},
	}
	var buf bytes.Buffer
	require.NoError(t, runVolumeLs(ident, userCfg, "/Users/me/projects/myproj", &buf))
	out := buf.String()
	assert.Contains(t, out, "myproj") // label: basename(macCwd)
	assert.Contains(t, out, "/home/devm/myproj")
	assert.Contains(t, out, filepath.Join(tmp, "Library", "Application Support", "devm-test", "p", "myproj"))
}

func TestRunVolumeLs_SecondaryRepoVolumeTrue_HasMacPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ident := identity.Config{Name: "devm-test"}
	userCfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"main":  {URL: strPtr("git@github.com:acme/primary.git"), Primary: boolPtr(true)},
			"extra": {URL: strPtr("git@github.com:acme/secondary.git"), Volume: boolPtr(true)},
		},
	}
	var buf bytes.Buffer
	require.NoError(t, runVolumeLs(ident, userCfg, "", &buf))
	out := buf.String()
	assert.Contains(t, out, filepath.Join(tmp, "Library", "Application Support", "devm-test", "p", "secondary"))
}

func TestRunVolumeLs_SecondaryRepoVolumeFalse_BlankMacPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ident := identity.Config{Name: "devm-test"}
	userCfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"main":  {URL: strPtr("git@github.com:acme/primary.git"), Primary: boolPtr(true)},
			"extra": {URL: strPtr("git@github.com:acme/secondary.git")}, // Volume unset → not mirrored
		},
	}
	var buf bytes.Buffer
	require.NoError(t, runVolumeLs(ident, userCfg, "", &buf))
	out := buf.String()

	var extraLine string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "extra" {
			extraLine = line
			break
		}
	}
	require.NotEmpty(t, extraLine, "expected a row for %q in:\n%s", "extra", out)
	assert.NotContains(t, extraLine, "Application Support")
	fields := strings.Fields(extraLine)
	// NAME LABEL KIND GUEST-PATH SIZE — MAC PATH is blank and collapses
	// out of the tab-separated fields entirely.
	require.Len(t, fields, 5)
	assert.Equal(t, "-", fields[4])
}

func TestRunVolumeLs_LabelCollision_ReturnsError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ident := identity.Config{Name: "devm-test"}
	userCfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Repos: map[string]schema.RepoConfig{
			"app": {URL: strPtr("git@github.com:acme/data.git"), Primary: boolPtr(true)},
		},
		Volumes: map[string]schema.Volume{
			// filepath.Base("/var/lib/data") == "data" == BareCloneName(data.git)
			"data": {Path: "/var/lib/data"},
		},
	}
	var buf bytes.Buffer
	err := runVolumeLs(ident, userCfg, "", &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `label "data"`)
	assert.Contains(t, err.Error(), "repos.app")
	assert.Contains(t, err.Error(), "volumes.data")
}
