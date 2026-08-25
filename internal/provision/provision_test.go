package provision

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStreamTart is a tartExecer that answers the first-boot marker probe
// from markerPresent and records every ExecStream call the rewritten
// RunOpen/RunEnforced make (each call appended to argvHistory/stdinHistory),
// optionally emitting scripted output lines and a scripted exit code / error
// on EVERY call.
type fakeStreamTart struct {
	markerPresent bool

	streamCalls  int
	argvHistory  [][]string
	stdinHistory [][]byte
	lastArgv     []string
	lastStdin    []byte

	// emit, when set, is called with the ExecStream onLine callback to
	// simulate the guest script's streamed stdout/stderr.
	emit      func(onLine func(stream, line string))
	exit      int
	streamErr error
}

func (f *fakeStreamTart) ExecWithRetry(_ context.Context, _ string, argv []string) tart.ExecResult {
	if len(argv) == 3 && argv[0] == "test" && argv[1] == "-f" && argv[2] == provisionedMarker {
		if f.markerPresent {
			return tart.ExecResult{ExitCode: 0}
		}
		return tart.ExecResult{ExitCode: 1}
	}
	return tart.ExecResult{ExitCode: 0}
}

func (f *fakeStreamTart) ExecStream(_ context.Context, _ string, stdin io.Reader,
	argv []string, onLine func(stream, line string)) (int, error) {
	f.streamCalls++
	f.lastArgv = argv
	f.argvHistory = append(f.argvHistory, argv)
	var body []byte
	if stdin != nil {
		body, _ = io.ReadAll(stdin)
	}
	f.lastStdin = body
	f.stdinHistory = append(f.stdinHistory, body)
	if f.emit != nil {
		f.emit(onLine)
	}
	return f.exit, f.streamErr
}

// scriptOf returns the composed guest script from the LAST recorded
// ExecStream argv (`bash -c <script>`).
func scriptOf(t *testing.T, f *fakeStreamTart) string {
	t.Helper()
	require.Len(t, f.lastArgv, 3, "ExecStream argv must be [bash -c <script>]")
	assert.Equal(t, "bash", f.lastArgv[0])
	assert.Equal(t, "-c", f.lastArgv[1])
	return f.lastArgv[2]
}

// scriptAt returns the composed guest script from the ExecStream argv at
// history index i (0-based).
func scriptAt(t *testing.T, f *fakeStreamTart, i int) string {
	t.Helper()
	require.Greater(t, len(f.argvHistory), i, "expected at least %d ExecStream calls", i+1)
	argv := f.argvHistory[i]
	require.Len(t, argv, 3, "ExecStream argv must be [bash -c <script>]")
	assert.Equal(t, "bash", argv[0])
	assert.Equal(t, "-c", argv[1])
	return argv[2]
}

func baseProvisioner(f *fakeStreamTart, cfg schema.Config) *Provisioner {
	return &Provisioner{
		Tart:            f,
		VMName:          "myproj-sbx",
		Cfg:             cfg,
		CARootPEM:       []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
		WorkspaceVMPath: "/Users/test/myproj",
	}
}

func TestRunOpen_ShipsExactlyOneExecStreamWithScriptAndTar(t *testing.T) {
	f := &fakeStreamTart{} // marker absent → first boot
	p := baseProvisioner(f, schema.Config{
		Project:  schema.Project{Name: "myproj"},
		Packages: []string{"jq"},
		Install:  []string{"echo hi"},
	})
	var buf bytes.Buffer
	require.NoError(t, p.RunOpen(context.Background(), &buf, nil))

	require.Equal(t, 1, f.streamCalls, "RunOpen must ship exactly ONE ExecStream")
	script := scriptOf(t, f)

	// The open script fail-fasts, writes the in-progress marker, extracts
	// the bundle, and runs the first-boot work — but does NOT enforce or
	// start the target (that's RunEnforced's job).
	assert.Contains(t, script, "set -eo pipefail")
	assert.Contains(t, script, "touch /run/devm/provisioning")
	assert.Contains(t, script, "sudo apt-get install -y 'jq'")
	assert.Contains(t, script, "/opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo hi'")
	assert.NotContains(t, script, "systemctl start devm.target")
	assert.NotContains(t, script, "touch /var/lib/devm/provisioned")
	assert.NotContains(t, script, "rm -f /run/devm/provisioning")

	// Stdin is the bundle tar; it must be a valid archive carrying the
	// devm-owned artifacts (install.sh + startup.sh).
	require.NotEmpty(t, f.lastStdin, "RunOpen's ExecStream stdin must carry the bundle tar")
	names := tarEntryNames(t, f.lastStdin)
	assert.Contains(t, names, "install.sh")
	assert.Contains(t, names, "startup.sh")
}

func TestRunEnforced_ShipsExactlyOneExecStreamNoStdin(t *testing.T) {
	f := &fakeStreamTart{}
	p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})
	p.firstBoot = true // simulates RunOpen having already set this
	var buf bytes.Buffer
	require.NoError(t, p.RunEnforced(context.Background(), &buf, nil))

	require.Equal(t, 1, f.streamCalls, "RunEnforced must ship exactly ONE ExecStream")
	script := scriptOf(t, f)

	assert.Contains(t, script, "set -eo pipefail")
	assert.Contains(t, script, "::devm:stage:enforce::")
	assert.Contains(t, script, "systemctl start devm.target")
	assert.Contains(t, script, "touch /var/lib/devm/provisioned")
	// Marker cleanup is the LAST line of the whole two-exec run.
	assert.Contains(t, script, "rm -f /run/devm/provisioning")
	// No bundle-extraction content — that already happened in RunOpen.
	assert.NotContains(t, script, "tar -xC /opt/devm")

	// No bundle on stdin.
	assert.Empty(t, f.lastStdin, "RunEnforced must not send the bundle tar — RunOpen already extracted it")
}

func tarEntryNames(t *testing.T, body []byte) []string {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(body))
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		names = append(names, h.Name)
	}
	return names
}

func TestRunOpen_ForwardsStreamedLinesToWriterAndOnLine(t *testing.T) {
	f := &fakeStreamTart{
		emit: func(onLine func(stream, line string)) {
			onLine("stdout", "::devm:stage:open::")
			onLine("stdout", "hello from guest")
			onLine("stderr", "a warning")
		},
	}
	p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})

	var buf bytes.Buffer
	var seen []string
	require.NoError(t, p.RunOpen(context.Background(), &buf, func(stream, line string) {
		seen = append(seen, stream+":"+line)
	}))

	// Every streamed line is captured to w AND forwarded to onLine.
	assert.Contains(t, buf.String(), "hello from guest")
	assert.Contains(t, buf.String(), "a warning")
	assert.Equal(t, []string{
		"stdout:::devm:stage:open::",
		"stdout:hello from guest",
		"stderr:a warning",
	}, seen)
}

func TestRunOpenAndEnforced_NonZeroExitClassifiesFailureByStage(t *testing.T) {
	tests := []struct {
		name         string
		failAtStage  string
		runEnforced  bool // true → the failure is simulated in RunEnforced's exec
		wantPostInst bool
	}{
		{"apt/install phase tears down", "install", false, false},
		{"docker phase tears down", "docker", false, false},
		{"templates phase tears down (runs pre-enforce, unenforced)", "templates", false, false},
		{"enforce phase tears down", "enforce", true, false},
		{"service phase keeps vm", "services", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stage := tc.failAtStage
			f := &fakeStreamTart{
				exit: 1,
				emit: func(onLine func(stream, line string)) {
					onLine("stdout", "::devm:stage:"+stage+"::")
					onLine("stderr", "boom")
				},
			}
			p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})

			var err error
			if tc.runEnforced {
				err = p.RunEnforced(context.Background(), io.Discard, nil)
			} else {
				err = p.RunOpen(context.Background(), io.Discard, nil)
			}
			require.Error(t, err)

			var sf *StepFailure
			require.ErrorAs(t, err, &sf)
			assert.Equal(t, stage, sf.Step, "failure must be tagged with the stage it reached")
			assert.Equal(t, tc.wantPostInst, IsPostInstallFailure(err))
		})
	}
}

func TestRunOpen_ExecStreamTransportErrorIsStepFailure(t *testing.T) {
	f := &fakeStreamTart{streamErr: context.DeadlineExceeded}
	p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})
	err := p.RunOpen(context.Background(), io.Discard, nil)
	require.Error(t, err)
	var sf *StepFailure
	require.ErrorAs(t, err, &sf)
}

func TestRunEnforced_ExecStreamTransportErrorIsStepFailure(t *testing.T) {
	f := &fakeStreamTart{streamErr: context.DeadlineExceeded}
	p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})
	err := p.RunEnforced(context.Background(), io.Discard, nil)
	require.Error(t, err)
	var sf *StepFailure
	require.ErrorAs(t, err, &sf)
}

func TestRunOpenThenEnforced_RestartOmitsFirstBootWork(t *testing.T) {
	f := &fakeStreamTart{markerPresent: true} // present → restart, not first boot
	p := baseProvisioner(f, schema.Config{
		Project:  schema.Project{Name: "myproj"},
		Packages: []string{"jq"},
		Install:  []string{"echo hi"},
	})
	require.NoError(t, p.RunOpen(context.Background(), io.Discard, nil))
	require.NoError(t, p.RunEnforced(context.Background(), io.Discard, nil))

	openScript := scriptAt(t, f, 0)
	// First-boot-only work must NOT appear on a restart.
	assert.NotContains(t, openScript, "apt-get install")
	assert.NotContains(t, openScript, "echo hi")
	assert.NotContains(t, openScript, "::devm:stage:packages::")
	// The guest-nft flush is unconditional — a restart with no first-boot
	// or open-stage work still needs the base image's policy-drop lock
	// cleared, or it would drop softnet's own egress.
	assert.Contains(t, openScript, "sudo nft flush ruleset")

	enforcedScript := scriptAt(t, f, 1)
	// And the completion marker is not re-written.
	assert.NotContains(t, enforcedScript, "touch /var/lib/devm/provisioned")
	// Enforcement + target still run every boot.
	assert.Contains(t, enforcedScript, "::devm:stage:enforce::")
	assert.Contains(t, enforcedScript, "systemctl start devm.target")
}

func TestRunEnforced_RoutingOnlyServiceOmittedButProcessServicesStarted(t *testing.T) {
	f := &fakeStreamTart{}
	p := baseProvisioner(f, schema.Config{
		Project: schema.Project{Name: "myproj"},
		Services: map[string]schema.Service{
			"routing-only": {Hostname: "x.test", Port: 8080},
			"with-exec":    {Exec: []string{"/bin/true"}},
		},
	})
	require.NoError(t, p.RunEnforced(context.Background(), io.Discard, nil))

	script := scriptOf(t, f)
	assert.Contains(t, script, "systemctl start with-exec.service")
	assert.NotContains(t, script, "routing-only.service")
}

func TestRunEnforced_MaskChownedToDevmBeforeMount(t *testing.T) {
	f := &fakeStreamTart{}
	p := baseProvisioner(f, schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"svc": {Exec: []string{"/bin/true"}},
		},
		Masks: []string{"data"},
	})
	p.WorkspaceVMPath = "/Users/x/proj"
	require.NoError(t, p.RunEnforced(context.Background(), io.Discard, nil))

	script := scriptOf(t, f)
	chown := "chown devm:devm '/var/devm/masks/p/data'"
	assert.Contains(t, script, chown)
	// chown must precede the bind mount, or the mount covers the target.
	chownIdx := strings.Index(script, chown)
	mountIdx := strings.Index(script, "mount --bind '/var/devm/masks/p/data'")
	require.Greater(t, chownIdx, 0)
	assert.Greater(t, mountIdx, chownIdx)
}

func TestRunOpen_TemplatesTriggerDispatcher(t *testing.T) {
	// devmbundle.Build renders declared templates from a real source file
	// under the repo root, so give it one.
	repoRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "x"), []byte("hi {{.Project.Name}}\n"), 0o644))

	f := &fakeStreamTart{}
	p := baseProvisioner(f, schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"svc": {Exec: []string{"/bin/true"}, Templates: []schema.Template{{Source: "x", Output: "/tmp/y"}}},
		},
	})
	p.WorkspaceVMPath = repoRoot
	require.NoError(t, p.RunOpen(context.Background(), io.Discard, nil))
	assert.Contains(t, scriptOf(t, f), "install-templates.sh")
}

func TestScriptInput_PopulatesGitCredentialsFromTopLevelRepo(t *testing.T) {
	url := "https://github.com/mdubb86/sewtrue.git"
	p := &Provisioner{
		Cfg: schema.Config{
			Repo: &schema.RepoConfig{URL: &url, Secret: "gh_token"},
		},
	}
	in := p.scriptInput()
	assert.Contains(t, in.GitCredentials,
		"https://x-access-token:__DEVM_SECRET_gh_token__@github.com/mdubb86/sewtrue.git")
	assert.Contains(t, in.GitConfig, "useHttpPath = true")
}

func TestScriptInput_PopulatesGitCredentialsFromPerVolumeRepo(t *testing.T) {
	topURL := "https://github.com/mdubb86/sewtrue.git"
	sdkURL := "https://github.com/mdubb86/some-sdk.git"
	p := &Provisioner{
		Cfg: schema.Config{
			Repo: &schema.RepoConfig{URL: &topURL, Secret: "gh_workspace"},
			Volumes: map[string]schema.Volume{
				"sdk": {
					Path: "/home/devm/sdk",
					Repo: &schema.RepoConfig{URL: &sdkURL, Secret: "gh_sdk_ro"},
				},
			},
		},
	}
	in := p.scriptInput()
	assert.Contains(t, in.GitCredentials,
		"https://x-access-token:__DEVM_SECRET_gh_workspace__@github.com/mdubb86/sewtrue.git")
	assert.Contains(t, in.GitCredentials,
		"https://x-access-token:__DEVM_SECRET_gh_sdk_ro__@github.com/mdubb86/some-sdk.git")
}

func TestScriptInput_PerVolumeInheritsTopLevelSecret(t *testing.T) {
	topURL := "https://github.com/mdubb86/sewtrue.git"
	sdkURL := "https://github.com/mdubb86/some-sdk.git"
	p := &Provisioner{
		Cfg: schema.Config{
			Repo: &schema.RepoConfig{URL: &topURL, Secret: "gh_shared"},
			Volumes: map[string]schema.Volume{
				// Per-volume with URL but empty Secret ⇒ inherits gh_shared.
				"sdk": {
					Path: "/home/devm/sdk",
					Repo: &schema.RepoConfig{URL: &sdkURL, Secret: ""},
				},
			},
		},
	}
	in := p.scriptInput()
	// Both lines reference the inherited secret name.
	assert.Contains(t, in.GitCredentials,
		"https://x-access-token:__DEVM_SECRET_gh_shared__@github.com/mdubb86/sewtrue.git")
	assert.Contains(t, in.GitCredentials,
		"https://x-access-token:__DEVM_SECRET_gh_shared__@github.com/mdubb86/some-sdk.git")
}

func TestScriptInput_NoReposEmptyCredentials(t *testing.T) {
	p := &Provisioner{Cfg: schema.Config{}}
	in := p.scriptInput()
	assert.Equal(t, "", in.GitCredentials, "no repos ⇒ no credentials lines")
	assert.Equal(t, "", in.GitConfig, "no repos ⇒ no gitconfig")
}

func TestScriptInput_DeterministicVolumeOrder(t *testing.T) {
	// Map iteration is unordered; the caller must sort volume names so
	// re-emits produce byte-identical files.
	urlA := "https://github.com/me/a.git"
	urlB := "https://github.com/me/b.git"
	urlC := "https://github.com/me/c.git"
	p := &Provisioner{
		Cfg: schema.Config{
			Volumes: map[string]schema.Volume{
				"c-vol": {Path: "/home/devm/c", Repo: &schema.RepoConfig{URL: &urlC, Secret: "s_c"}},
				"a-vol": {Path: "/home/devm/a", Repo: &schema.RepoConfig{URL: &urlA, Secret: "s_a"}},
				"b-vol": {Path: "/home/devm/b", Repo: &schema.RepoConfig{URL: &urlB, Secret: "s_b"}},
			},
		},
	}
	// Run scriptInput twice; assert identical output.
	in1 := p.scriptInput()
	in2 := p.scriptInput()
	assert.Equal(t, in1.GitCredentials, in2.GitCredentials,
		"same Cfg must yield byte-identical credentials output across calls")
	// Assert sorted volume order in the emitted lines.
	// Expected order: primary (none here) then a-vol, b-vol, c-vol.
	aIdx := strings.Index(in1.GitCredentials, "/me/a.git")
	bIdx := strings.Index(in1.GitCredentials, "/me/b.git")
	cIdx := strings.Index(in1.GitCredentials, "/me/c.git")
	assert.True(t, aIdx < bIdx && bIdx < cIdx,
		"volume-repo lines must be in sorted volume-name order (a-vol < b-vol < c-vol)")
}

func TestProvisioner_ScriptInput_PassesScripts(t *testing.T) {
	p := &Provisioner{
		Cfg: schema.Config{
			Project: schema.Project{Name: "p"},
			Install: []string{">install-supabase"},
			Scripts: map[string][]string{
				"install-supabase": {"echo one", "echo two"},
			},
		},
		firstBoot: true,
	}
	in := p.scriptInput()
	assert.Equal(t, []string{"echo one", "echo two"}, in.Scripts["install-supabase"])
	assert.Equal(t, []string{">install-supabase"}, in.Install)
}

// makeRepoWithIdentity creates a fixture git repo at a temp dir with a
// local (repo-scoped) user.name/user.email, so the read is deterministic
// regardless of the test machine's own global git config.
func makeRepoWithIdentity(t *testing.T, name, email string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", dir, "init", "-q").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "user.name", name).Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "user.email", email).Run())
	return dir
}

func TestGitIdentity_ReadsRepoLocalConfig(t *testing.T) {
	dir := makeRepoWithIdentity(t, "Fixture User", "fixture@example.com")
	p := &Provisioner{MacCwd: dir}
	id := p.gitIdentity()
	assert.Equal(t, "Fixture User", id.UserName)
	assert.Equal(t, "fixture@example.com", id.UserEmail)
}

func TestGitIdentity_EmptyMacCwdSkipsRead(t *testing.T) {
	p := &Provisioner{}
	id := p.gitIdentity()
	assert.Equal(t, "", id.UserName)
	assert.Equal(t, "", id.UserEmail)
}

func TestGitIdentity_NoIdentityAnywhereYieldsEmpty(t *testing.T) {
	// Not a git repo, and isolated from the test machine's own global/system
	// git config, so this reflects a genuinely identity-less environment
	// rather than leaking whatever the dev machine happens to have set.
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	p := &Provisioner{MacCwd: dir}
	id := p.gitIdentity()
	assert.Equal(t, "", id.UserName)
	assert.Equal(t, "", id.UserEmail)
}

func TestScriptInput_GitConfigCarriesIdentityWhenReposDeclared(t *testing.T) {
	dir := makeRepoWithIdentity(t, "Fixture User", "fixture@example.com")
	url := "https://github.com/mdubb86/sewtrue.git"
	p := &Provisioner{
		MacCwd: dir,
		Cfg: schema.Config{
			Repo: &schema.RepoConfig{URL: &url, Secret: "gh_token"},
		},
	}
	in := p.scriptInput()
	assert.Contains(t, in.GitConfig, "[user]\n    name = Fixture User\n    email = fixture@example.com\n")
}
