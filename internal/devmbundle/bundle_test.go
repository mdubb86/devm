package devmbundle

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	body, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
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
	body, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
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
	a, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)
	b, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestBuild_TemplatePathsAreFlatBaseNames(t *testing.T) {
	// RenderTemplates returns a map keyed by absolute host paths
	// (<repoRoot>/.devm/templates/NN-svc-base.sh); Build must reduce
	// them to a flat basename so the guest's install-templates.sh
	// dispatcher (which iterates templates/*.sh non-recursively) can
	// find them. Regression: an earlier revision embedded the full
	// host path into the tar entry name and silently broke the whole
	// templates flow.
	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "tmpl"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "tmpl", "nginx.conf"), []byte("hello {{.Project.ID}}"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Services: map[string]schema.Service{
			"web": {
				Templates: []schema.Template{{
					Source: "tmpl/nginx.conf",
					Output: "/etc/nginx/nginx.conf",
				}},
			},
		},
	}
	body, err := Build(BuildInput{Cfg: cfg, RepoRoot: repoRoot})
	require.NoError(t, err)

	entries := readTar(t, body)
	found := false
	for name := range entries {
		if !strings.HasPrefix(name, "templates/") {
			continue
		}
		found = true
		rest := name[len("templates/"):]
		require.Falsef(t, strings.Contains(rest, "/"),
			"template entry name must be a flat basename, got %q", name)
	}
	require.True(t, found, "expected at least one templates/ entry in the bundle")
}

type tarEntry struct {
	mode int64
	uid  int64
	gid  int64
	body []byte
}

func TestBuild_TakesBuildInput_Compat(t *testing.T) {
	// Same inputs as the old (cfg, repoRoot) form should yield the same tar.
	cfg := schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}}
	in := BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"}
	got, err := Build(in)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	// Assert the tar has the pre-existing entries and no new junk yet.
	names := tarEntryNames(t, got)
	assert.Contains(t, names, ".env")
	assert.Contains(t, names, "install.sh")
	assert.Contains(t, names, "scripts/with-devm-env")
}

// tarEntryNames helper — reuse or add:
func tarEntryNames(t *testing.T, blob []byte) []string {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(blob))
	var out []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		out = append(out, h.Name)
	}
	return out
}

func TestBuild_TarContainsCA(t *testing.T) {
	in := BuildInput{
		Cfg:       schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}},
		RepoRoot:  "/tmp/repo",
		CARootPEM: []byte("-----BEGIN CERTIFICATE-----\nDUMMYDATA\n-----END CERTIFICATE-----\n"),
	}
	blob, err := Build(in)
	require.NoError(t, err)

	body := readTarEntry(t, blob, "ca/devm.crt")
	assert.Equal(t, string(in.CARootPEM), string(body))
}

// readTarEntry helper — reuse or add:
func readTarEntry(t *testing.T, blob []byte, name string) []byte {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(blob))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if h.Name == name {
			data, err := io.ReadAll(tr)
			require.NoError(t, err)
			return data
		}
	}
	t.Fatalf("entry %q not found in tar", name)
	return nil
}

func TestBuild_TarContainsCaddyfile(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Services: map[string]schema.Service{
			"web": {Hostname: "web.local", Port: 8080},
		},
	}
	blob, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)
	body := readTarEntry(t, blob, "caddy/Caddyfile")
	assert.Contains(t, string(body), "web.local")
	assert.Contains(t, string(body), "8080")
}

func TestBuild_TarContainsDnsmasqDropIn(t *testing.T) {
	blob, err := Build(BuildInput{
		Cfg:      schema.Config{Project: schema.Project{ID: "p", VMName: "p-vm"}},
		RepoRoot: "/tmp/repo",
	})
	require.NoError(t, err)
	body := readTarEntry(t, blob, "dnsmasq/devm-test.conf")
	assert.NotEmpty(t, body)
}

func TestBuild_TarContainsServiceUnits(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Services: map[string]schema.Service{
			"web":     {Exec: []string{"/bin/true"}, Hostname: "w.local", Port: 80},
			"routing": {Hostname: "r.local", Port: 81}, // no Exec/Systemd — skipped
		},
	}
	blob, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)
	names := tarEntryNames(t, blob)
	assert.Contains(t, names, "systemd/web.service")
	assert.NotContains(t, names, "systemd/routing.service")
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
