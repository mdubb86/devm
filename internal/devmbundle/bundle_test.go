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
		Project: schema.Project{Name: "p"},
		Env: map[string]schema.EnvValue{
			"FOO": {Literal: "bar"},
		},
	}
	body, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)

	entries := readTar(t, body)
	want := map[string]int64{
		"etc/environment":              0o644,
		"scripts/with-devm-env":        0o755,
		"scripts/install-templates.sh": 0o755,
		"install.sh":                   0o755,
		"startup.sh":                   0o755,
		"GUEST.md":                     0o644,
	}
	for path, mode := range want {
		e, ok := entries[path]
		require.True(t, ok, "bundle missing %s", path)
		assert.Equal(t, mode, e.mode&0o777, "%s mode", path)
		assert.Equal(t, int64(0), e.uid, "%s uid", path)
		assert.Equal(t, int64(0), e.gid, "%s gid", path)
	}
}

// TestBuild_GuestDocContent pins that the guest-perspective doc is
// bundled and includes the load-bearing content (mode-detect env var,
// the mutagen sync model). Guest-side agents lean on this file to
// distinguish host-only instructions from guest-safe ones — the
// CLAUDE.md pointer in internal/skills/devm.md points at it
// explicitly.
func TestBuild_GuestDocContent(t *testing.T) {
	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	body, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)

	entries := readTar(t, body)
	e, ok := entries["GUEST.md"]
	require.True(t, ok, "bundle missing GUEST.md")
	doc := string(e.body)
	assert.Contains(t, doc, "IS_SANDBOX", "GUEST.md must document the mode-detect env")
	assert.Contains(t, doc, "mutagen", "GUEST.md must explain the mutagen sync model")
	assert.Contains(t, doc, "iron-proxy", "GUEST.md must explain the egress model")
}

// TestBuild_InstallScriptSeedsNSSTrust pins that install.sh in the
// bundle seeds the devm user's per-user NSS db with the devm CA.
// Chromium (Debian's package AND Playwright's Chrome-for-Testing)
// reads $HOME/.pki/nssdb and does NOT fall through to /etc/pki/nssdb
// when the per-user db exists but lacks the cert. A regression that
// dropped this seed would silently break every browser in the guest
// against .test hostnames and every iron-proxy-MITM'd HTTPS site.
func TestBuild_InstallScriptSeedsNSSTrust(t *testing.T) {
	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	body, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)

	entries := readTar(t, body)
	e, ok := entries["install.sh"]
	require.True(t, ok, "bundle missing install.sh")
	script := string(e.body)
	// The seed must run as the devm user so $HOME resolves to
	// /home/devm and files land with correct ownership. install.sh
	// itself runs as root via `sudo /opt/devm/install.sh`.
	assert.Contains(t, script, "su - devm -c",
		"install.sh must run the NSS seed in the devm user's context")
	assert.Contains(t, script, `certutil -A -n devm -t "C,,"`,
		"install.sh must seed the devm CA into the per-user NSS db")
	assert.Contains(t, script, `sql:"$HOME/.pki/nssdb"`,
		"install.sh must target the per-user NSS trust db path ($HOME/.pki/nssdb)")
	// The per-user db must NOT be world-readable — 0700/0600. Prod
	// docs suggest tight perms for user-scoped NSS stores; the store
	// holds only a public CA cert today but the mode should match
	// $HOME convention.
	assert.Contains(t, script, `chmod 700 "$HOME/.pki" "$HOME/.pki/nssdb"`,
		"install.sh must chmod the NSS dir 0700")
}

// TestBuild_InstallScriptDropsFirefoxPoliciesJSON pins that install.sh
// writes /etc/firefox/policies/policies.json with a SecurityDevices
// entry pointing at p11-kit-trust.so — the bridge that lets Firefox
// (and Firefox-derived browsers like Camoufox, once symlinked) trust
// the system CA store transparently, without per-profile certutil
// dances. Without this drop-in, Firefox uses its own bundled NSS with
// only Mozilla's root set, and iron-proxy-MITM'd HTTPS shows
// SEC_ERROR_UNKNOWN_ISSUER.
func TestBuild_InstallScriptDropsFirefoxPoliciesJSON(t *testing.T) {
	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	body, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)

	entries := readTar(t, body)
	e, ok := entries["install.sh"]
	require.True(t, ok, "bundle missing install.sh")
	script := string(e.body)

	assert.Contains(t, script, "/etc/firefox/policies/policies.json",
		"install.sh must write the Firefox enterprise-policy file at the canonical Linux path")
	assert.Contains(t, script, "SecurityDevices",
		"install.sh must set the SecurityDevices policy so Firefox loads a PKCS#11 module")
	assert.Contains(t, script, "p11-kit-trust.so",
		"install.sh must point SecurityDevices at p11-kit-trust.so — the NSS module that bridges the system CA store into Firefox")
}

func TestBuild_EnvReflectsConfig(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Env: map[string]schema.EnvValue{
			"MYVAR": {Literal: "myval"},
		},
	}
	body, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)

	entries := readTar(t, body)
	envBody := string(entries["etc/environment"].body)
	assert.Contains(t, envBody, "MYVAR")
	assert.Contains(t, envBody, "myval")
}

func TestBuild_Deterministic(t *testing.T) {
	// Two builds of the same cfg must produce byte-identical tars —
	// required so future callers can gate re-pipe on content hash
	// without spurious drift.
	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	a, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)
	b, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestBuild_TemplatePathsAreFlatBaseNames(t *testing.T) {
	// RenderTemplates returns a map keyed by absolute paths under the
	// daemon runtime dir (<daemonRuntimeDir>/templates/<project>/
	// NN-svc-base.sh); Build must reduce them to a flat basename so the
	// guest's install-templates.sh dispatcher (which iterates
	// templates/*.sh non-recursively) can
	// find them. Regression: an earlier revision embedded the full
	// host path into the tar entry name and silently broke the whole
	// templates flow.
	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, "tmpl"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "tmpl", "nginx.conf"), []byte("hello {{.Project.Name}}"), 0o644))

	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
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
	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	in := BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"}
	got, err := Build(in)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	// Assert the tar has the pre-existing entries and no new junk yet.
	names := tarEntryNames(t, got)
	assert.Contains(t, names, "etc/environment")
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
		Cfg:       schema.Config{Project: schema.Project{Name: "p"}},
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

// TestBuild_TarHasNoCaddy pins Caddy's removal: `.test` routing is the
// daemon's guest-origin listener, so no Caddyfile should ever enter the
// bundle.
func TestBuild_TarHasNoCaddy(t *testing.T) {
	cfg := schema.Config{Services: map[string]schema.Service{
		"api": {Hostname: "api.test", Port: 3000},
	}}
	b, err := Build(BuildInput{Cfg: cfg})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, name := range tarEntryNames(t, b) {
		if strings.Contains(name, "caddy") {
			t.Fatalf("bundle still carries a caddy entry: %s", name)
		}
	}
}

func TestBuild_TarContainsDnsmasqDropIn(t *testing.T) {
	blob, err := Build(BuildInput{
		Cfg:      schema.Config{Project: schema.Project{Name: "p"}},
		RepoRoot: "/tmp/repo",
	})
	require.NoError(t, err)
	body := readTarEntry(t, blob, "dnsmasq/devm-test.conf")
	assert.NotEmpty(t, body)
}

func TestBuild_TarContainsServiceUnits(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
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

func TestBuild_ServiceUnit_InheritsCfgEnv(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Env: map[string]schema.EnvValue{
			"GITHUB_TOKEN": {Literal: "xyz"}, // cfg-level env
		},
		Services: map[string]schema.Service{
			"web": {
				Exec: []string{"/bin/true"}, // eligible for a unit
				// no per-service env — the cfg-level entry must reach the rendered unit
			},
		},
	}
	blob, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)
	unit := readTarEntry(t, blob, "systemd/web.service")
	// Rendered unit should carry an Environment= line for GITHUB_TOKEN.
	// Regression: cfg.Env used to merge into svc.Env before RenderService;
	// Task 5 dropped that merge and this pinning test locks it back in.
	assert.Contains(t, string(unit), "GITHUB_TOKEN",
		"top-level env entries must reach rendered systemd units")
	assert.Contains(t, string(unit), "xyz")
}

func TestBuild_ServiceUnit_PerServiceEnvOverridesCfg(t *testing.T) {
	// Same key in cfg.Env and svc.Env → svc.Env wins.
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Env:     map[string]schema.EnvValue{"K": {Literal: "cfg-value"}},
		Services: map[string]schema.Service{
			"web": {
				Exec: []string{"/bin/true"},
				Env:  map[string]schema.EnvValue{"K": {Literal: "svc-value"}},
			},
		},
	}
	blob, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)
	unit := readTarEntry(t, blob, "systemd/web.service")
	assert.Contains(t, string(unit), "svc-value")
	assert.NotContains(t, string(unit), "cfg-value",
		"per-service env must override cfg-level env on collision")
}

func TestBuild_TarContainsDockerShims_WhenDockerTrue(t *testing.T) {
	blob, err := Build(BuildInput{
		Cfg:            schema.Config{Project: schema.Project{Name: "p"}, Docker: true},
		RepoRoot:       "/tmp/repo",
		DockerRuncShim: []byte("runc-shim-elf"),
		DockerCLIShim:  []byte("docker-shim-elf"),
	})
	require.NoError(t, err)
	names := tarEntryNames(t, blob)
	assert.Contains(t, names, "bin/devm-runc-shim")
	assert.Contains(t, names, "bin/docker")
	assert.Equal(t, []byte("runc-shim-elf"), readTarEntry(t, blob, "bin/devm-runc-shim"))
}

func TestBuild_TarOmitsDockerShims_WhenDockerFalse(t *testing.T) {
	blob, err := Build(BuildInput{
		Cfg:            schema.Config{Project: schema.Project{Name: "p"}, Docker: false},
		RepoRoot:       "/tmp/repo",
		DockerRuncShim: []byte("runc-shim-elf"),
		DockerCLIShim:  []byte("docker-shim-elf"),
	})
	require.NoError(t, err)
	names := tarEntryNames(t, blob)
	assert.NotContains(t, names, "bin/devm-runc-shim")
	assert.NotContains(t, names, "bin/docker")
}

func TestBuild_IncludesPopWhenPresent(t *testing.T) {
	blob, err := Build(BuildInput{
		Cfg:      schema.Config{Project: schema.Project{Name: "p"}},
		RepoRoot: "/tmp/repo",
		Pop:      []byte("pop-elf-bytes"),
	})
	require.NoError(t, err)
	names := tarEntryNames(t, blob)
	assert.Contains(t, names, "bin/pop")
	assert.Equal(t, []byte("pop-elf-bytes"), readTarEntry(t, blob, "bin/pop"))
}

func TestBuild_OmitsPopWhenAbsent(t *testing.T) {
	blob, err := Build(BuildInput{
		Cfg:      schema.Config{Project: schema.Project{Name: "p"}},
		RepoRoot: "/tmp/repo",
	})
	require.NoError(t, err)
	names := tarEntryNames(t, blob)
	assert.NotContains(t, names, "bin/pop")
}

func TestBuild_TarContainsStartupScript_WhenStartupSet(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Startup: []string{"echo hi"},
		Services: map[string]schema.Service{
			"web": {Exec: []string{"/bin/true"}},
		},
	}
	blob, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)

	names := tarEntryNames(t, blob)
	assert.Contains(t, names, "startup.sh")
	assert.NotContains(t, names, "systemd/devm-startup.service")
	assert.NotContains(t, names, "systemd/devm-enforce.service")

	startupScript := readTarEntry(t, blob, "startup.sh")
	assert.Contains(t, string(startupScript), "echo hi")

	// Declared service units join devm.target.
	webUnit := readTarEntry(t, blob, "systemd/web.service")
	assert.Contains(t, string(webUnit), "WantedBy=devm.target")
}

func TestBuild_AlwaysEmitsStartupScript_WhenStartupUnset(t *testing.T) {
	// The startup.sh mechanism is always registered, for every project
	// — not opt-in on startup: being set. An empty cfg.Startup still
	// gets startup.sh; it's just a no-op script.
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"web": {Exec: []string{"/bin/true"}},
		},
	}
	blob, err := Build(BuildInput{Cfg: cfg, RepoRoot: "/tmp/repo"})
	require.NoError(t, err)

	names := tarEntryNames(t, blob)
	assert.Contains(t, names, "startup.sh")
	assert.NotContains(t, names, "systemd/devm-startup.service")
	assert.NotContains(t, names, "systemd/devm-enforce.service")

	startupScript := readTarEntry(t, blob, "startup.sh")
	assert.Equal(t, "#!/bin/bash\nset -eo pipefail\n", string(startupScript))

	webUnit := readTarEntry(t, blob, "systemd/web.service")
	assert.Contains(t, string(webUnit), "WantedBy=devm.target",
		"declared service units join devm.target, startup: set or not")
}

// TestBuild_IncludesEtcProfileDevm proves the bundle carries the
// login-shell PATH restorer that install.sh drops at
// /etc/profile.d/devm.sh. Regression pin: without it, `bash -lc
// <cmd>` inside the guest can't find devm's tools.
func TestBuild_IncludesEtcProfileDevm(t *testing.T) {
	body, err := Build(BuildInput{
		Cfg: schema.Config{
			Project: schema.Project{Name: "p"},
		},
	})
	require.NoError(t, err)
	entries := readTar(t, body)
	entry, ok := entries["profile.d/devm.sh"]
	require.True(t, ok, "bundle must contain profile.d/devm.sh")
	assert.Equal(t, int64(0o644), entry.mode)
	// Regression: /etc/profile.d/devm.sh must source /etc/environment
	// via `set -a` so every login shell inherits devm's env — the PATH
	// restore after /etc/profile rebuild, CA trust vars, UV_SYSTEM_CERTS.
	// The `. /etc/environment` and `set -a` assertions pin the loader
	// mechanism directly since it's what makes downstream consumers work.
	assert.Contains(t, string(entry.body), ". /etc/environment")
	assert.Contains(t, string(entry.body), "set -a")
	// Regression: interactive SSH (bash -l) must land in the workspace
	// directory, not $HOME. Without the cd, running `supabase start` (or
	// any tool that treats cwd as the project root) initializes a fresh
	// project under /home/devm instead of picking up the checkout.
	// with-devm-env handles the same chdir for devm exec / devm shell.
	assert.Contains(t, string(entry.body), `cd "$WORKSPACE"`,
		"profile.d/devm.sh must chdir to $WORKSPACE so SSH login shells land in the project root")
}

// TestBuild_IncludesEtcEnvironment proves the bundle carries the
// machine-wide env file that install.sh drops at /etc/environment,
// where pam_env sources it for every ssh session (including raw
// non-interactive `ssh host cmd`). Without this, devm's PATH and
// cfg.Env don't reach non-wrapper shells.
func TestBuild_IncludesEtcEnvironment(t *testing.T) {
	body, err := Build(BuildInput{
		Cfg: schema.Config{Project: schema.Project{Name: "p"}},
	})
	require.NoError(t, err)
	entries := readTar(t, body)
	entry, ok := entries["etc/environment"]
	require.True(t, ok, "bundle must contain etc/environment")
	assert.Equal(t, int64(0o644), entry.mode)
	assert.Contains(t, string(entry.body), "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt")
	assert.Contains(t, string(entry.body), "PATH=")
}

// TestBuild_EtcEnvironmentErrorPropagates proves an invalid env value
// (raw newline — pam_env unrepresentable) causes Build to return the
// render error rather than emit a subtly broken /etc/environment.
func TestBuild_EtcEnvironmentErrorPropagates(t *testing.T) {
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Env: map[string]schema.EnvValue{
			"BAD": {Literal: "line1\nline2"},
		},
	}
	_, err := Build(BuildInput{Cfg: cfg})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BAD")
}

func TestBuild_TarContainsSSHMaterial(t *testing.T) {
	blob, err := Build(BuildInput{
		Cfg:                 schema.Config{Project: schema.Project{Name: "p"}},
		RepoRoot:            "/tmp/repo",
		SSHAuthorizedPubkey: []byte("ssh-ed25519 AAAA...\n"),
		SSHHostPriv:         []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n..."),
		SSHHostPub:          []byte("ssh-ed25519 BBBB...\n"),
	})
	require.NoError(t, err)
	names := tarEntryNames(t, blob)
	assert.Contains(t, names, "ssh/authorized_keys")
	assert.Contains(t, names, "ssh/ssh_host_ed25519_key")
	assert.Contains(t, names, "ssh/ssh_host_ed25519_key.pub")
}
