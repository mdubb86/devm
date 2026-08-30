package provision

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStreamTart is a tartExecer that answers the first-boot marker probe
// from markerPresent and records every ExecStream call RunBundle/RunUser/
// RunEnforced make (each call appended to argvHistory/stdinHistory),
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

func TestRunBundle_ShipsExactlyOneExecStreamWithScriptAndTar(t *testing.T) {
	f := &fakeStreamTart{} // marker absent → first boot
	p := baseProvisioner(f, schema.Config{
		Project:  schema.Project{Name: "myproj"},
		Packages: []string{"jq"},
		Install:  []string{"echo hi"},
	})
	var buf bytes.Buffer
	require.NoError(t, p.RunBundle(context.Background(), &buf, nil))

	require.Equal(t, 1, f.streamCalls, "RunBundle must ship exactly ONE ExecStream")
	script := scriptOf(t, f)

	// The bundle script fail-fasts, writes the in-progress marker, extracts
	// the bundle, and runs install.sh — but has no user-phase content
	// (that's RunUser's job) and does NOT enforce or start the target
	// (that's RunEnforced's job).
	assert.Contains(t, script, "set -eo pipefail")
	assert.Contains(t, script, "touch /run/devm/provisioning")
	assert.Contains(t, script, "sudo tar -xC /opt/devm")
	assert.Contains(t, script, "sudo /opt/devm/install.sh")
	assert.NotContains(t, script, "sudo apt-get install -y 'jq'")
	assert.NotContains(t, script, "/opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo hi'")
	assert.NotContains(t, script, "systemctl start devm.target")
	assert.NotContains(t, script, "touch /var/lib/devm/provisioned")
	assert.NotContains(t, script, "rm -f /run/devm/provisioning")

	// Stdin is the bundle tar; it must be a valid archive carrying the
	// devm-owned artifacts (install.sh + startup.sh).
	require.NotEmpty(t, f.lastStdin, "RunBundle's ExecStream stdin must carry the bundle tar")
	names := tarEntryNames(t, f.lastStdin)
	assert.Contains(t, names, "install.sh")
	assert.Contains(t, names, "startup.sh")
}

func TestRunUser_ShipsExactlyOneExecStreamNoStdin(t *testing.T) {
	f := &fakeStreamTart{} // marker absent → first boot
	p := baseProvisioner(f, schema.Config{
		Project:  schema.Project{Name: "myproj"},
		Packages: []string{"jq"},
		Install:  []string{"echo hi"},
	})
	p.firstBoot = true // simulates RunBundle having already set this
	var buf bytes.Buffer
	require.NoError(t, p.RunUser(context.Background(), &buf, nil))

	require.Equal(t, 1, f.streamCalls, "RunUser must ship exactly ONE ExecStream")
	script := scriptOf(t, f)

	assert.Contains(t, script, "set -eo pipefail")
	assert.Contains(t, script, "sudo apt-get install -y 'jq'")
	assert.Contains(t, script, "/opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo hi'")
	// No bundle-extraction content — that already happened in RunBundle.
	assert.NotContains(t, script, "tar -xC /opt/devm")
	assert.NotContains(t, script, "/opt/devm/install.sh")
	assert.NotContains(t, script, "nft flush ruleset")
	assert.NotContains(t, script, "touch /run/devm/provisioning")
	assert.NotContains(t, script, "systemctl start devm.target")
	assert.NotContains(t, script, "touch /var/lib/devm/provisioned")

	// No bundle on stdin.
	assert.Empty(t, f.lastStdin, "RunUser must not send the bundle tar — RunBundle already extracted it")
}

func TestRunEnforced_ShipsExactlyOneExecStreamNoStdin(t *testing.T) {
	f := &fakeStreamTart{}
	p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})
	p.firstBoot = true // simulates RunBundle having already set this
	var buf bytes.Buffer
	require.NoError(t, p.RunEnforced(context.Background(), &buf, nil))

	require.Equal(t, 1, f.streamCalls, "RunEnforced must ship exactly ONE ExecStream")
	script := scriptOf(t, f)

	assert.Contains(t, script, "set -eo pipefail")
	assert.Contains(t, script, "::devm:stage:enforce::")
	assert.Contains(t, script, "systemctl start devm.target")
	assert.Contains(t, script, "touch /var/lib/devm/provisioned")
	// Marker cleanup is the LAST line of the whole three-exec run.
	assert.Contains(t, script, "rm -f /run/devm/provisioning")
	// No bundle-extraction content — that already happened in RunBundle.
	assert.NotContains(t, script, "tar -xC /opt/devm")

	// No bundle on stdin.
	assert.Empty(t, f.lastStdin, "RunEnforced must not send the bundle tar — RunBundle already extracted it")
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

func TestRunBundle_ForwardsStreamedLinesToWriterAndOnLine(t *testing.T) {
	f := &fakeStreamTart{
		emit: func(onLine func(stream, line string)) {
			onLine("stdout", "::devm:stage:bundle::")
			onLine("stdout", "hello from guest")
			onLine("stderr", "a warning")
		},
	}
	p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})

	var buf bytes.Buffer
	var seen []string
	require.NoError(t, p.RunBundle(context.Background(), &buf, func(stream, line string) {
		seen = append(seen, stream+":"+line)
	}))

	// Every streamed line is captured to w AND forwarded to onLine.
	assert.Contains(t, buf.String(), "hello from guest")
	assert.Contains(t, buf.String(), "a warning")
	assert.Equal(t, []string{
		"stdout:::devm:stage:bundle::",
		"stdout:hello from guest",
		"stderr:a warning",
	}, seen)
}

func TestRunBundleUserEnforced_NonZeroExitClassifiesFailureByStage(t *testing.T) {
	tests := []struct {
		name         string
		failAtStage  string
		runPhase     string // "bundle" | "user" | "enforced" — which exec the failure is simulated in
		wantPostInst bool
	}{
		{"bundle-extract phase tears down", "bundle", "bundle", false},
		{"apt/install phase tears down", "install", "user", false},
		{"docker phase tears down", "docker", "user", false},
		{"templates phase tears down (runs pre-enforce, unenforced)", "templates", "user", false},
		{"enforce phase tears down", "enforce", "enforced", false},
		{"service phase keeps vm", "services", "enforced", true},
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
			switch tc.runPhase {
			case "bundle":
				err = p.RunBundle(context.Background(), io.Discard, nil)
			case "user":
				err = p.RunUser(context.Background(), io.Discard, nil)
			case "enforced":
				err = p.RunEnforced(context.Background(), io.Discard, nil)
			}
			require.Error(t, err)

			var sf *StepFailure
			require.ErrorAs(t, err, &sf)
			assert.Equal(t, stage, sf.Step, "failure must be tagged with the stage it reached")
			assert.Equal(t, tc.wantPostInst, IsPostInstallFailure(err))
		})
	}
}

func TestRunBundle_ExecStreamTransportErrorIsStepFailure(t *testing.T) {
	f := &fakeStreamTart{streamErr: context.DeadlineExceeded}
	p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})
	err := p.RunBundle(context.Background(), io.Discard, nil)
	require.Error(t, err)
	var sf *StepFailure
	require.ErrorAs(t, err, &sf)
}

func TestRunUser_ExecStreamTransportErrorIsStepFailure(t *testing.T) {
	f := &fakeStreamTart{streamErr: context.DeadlineExceeded}
	p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})
	err := p.RunUser(context.Background(), io.Discard, nil)
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

func TestRunBundleUserEnforced_RestartOmitsFirstBootWork(t *testing.T) {
	f := &fakeStreamTart{markerPresent: true} // present → restart, not first boot
	p := baseProvisioner(f, schema.Config{
		Project:  schema.Project{Name: "myproj"},
		Packages: []string{"jq"},
		Install:  []string{"echo hi"},
	})
	require.NoError(t, p.RunBundle(context.Background(), io.Discard, nil))
	require.NoError(t, p.RunUser(context.Background(), io.Discard, nil))
	require.NoError(t, p.RunEnforced(context.Background(), io.Discard, nil))

	bundleScript := scriptAt(t, f, 0)
	// The guest-nft flush is unconditional — a restart with no first-boot
	// or user-phase work still needs the base image's policy-drop lock
	// cleared, or it would drop softnet's own egress.
	assert.Contains(t, bundleScript, "sudo nft flush ruleset")

	userScript := scriptAt(t, f, 1)
	// First-boot-only work must NOT appear on a restart.
	assert.NotContains(t, userScript, "apt-get install")
	assert.NotContains(t, userScript, "echo hi")
	assert.NotContains(t, userScript, "::devm:stage:packages::")

	enforcedScript := scriptAt(t, f, 2)
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

func TestRunBundle_SucceedsWithTemplatesDeclared(t *testing.T) {
	// devmbundle.Build (called by buildBundle, inside RunBundle) renders
	// declared templates from a real source file under the repo root, so
	// give it one.
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
	require.NoError(t, p.RunBundle(context.Background(), io.Discard, nil))
}

func TestRunUser_TemplatesTriggerDispatcher(t *testing.T) {
	f := &fakeStreamTart{}
	p := baseProvisioner(f, schema.Config{
		Project: schema.Project{Name: "p"},
		Services: map[string]schema.Service{
			"svc": {Exec: []string{"/bin/true"}, Templates: []schema.Template{{Source: "x", Output: "/tmp/y"}}},
		},
	})
	require.NoError(t, p.RunUser(context.Background(), io.Discard, nil))
	assert.Contains(t, scriptOf(t, f), "install-templates.sh")
}

func TestScriptInput_NoReposEmptyCredentials(t *testing.T) {
	p := &Provisioner{Cfg: schema.Config{}}
	in := p.scriptInput()
	assert.Equal(t, "", in.GitCredentials, "no repos ⇒ no credentials lines")
	assert.Equal(t, "", in.GitConfig, "no repos ⇒ no gitconfig")
}

func TestScriptInput_PopulatesGitCredentialsFromReposMap(t *testing.T) {
	url := "https://github.com/mdubb86/sewtrue.git"
	p := &Provisioner{
		Cfg: schema.Config{
			Repos: map[string]schema.RepoConfig{
				"workspace": {URL: &url, Secret: "gh_token"},
			},
		},
	}
	in := p.scriptInput()
	assert.Contains(t, in.GitCredentials,
		"https://x-access-token:__DEVM_SECRET_gh_token__@github.com/mdubb86/sewtrue.git")
	assert.Contains(t, in.GitConfig, "useHttpPath = true")
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

func TestScriptInput_IdentityOnlyEmitsGitconfigNotCredentials(t *testing.T) {
	dir := makeRepoWithIdentity(t, "Fixture User", "fixture@example.com")
	p := &Provisioner{
		MacCwd: dir,
		Cfg:    schema.Config{},
	}
	in := p.scriptInput()
	assert.NotEmpty(t, in.GitConfig, "identity alone must still emit a gitconfig")
	assert.Contains(t, in.GitConfig, "[user]\n    name = Fixture User\n    email = fixture@example.com\n")
	assert.Equal(t, "", in.GitCredentials, "no repo bindings ⇒ no .git-credentials lines")
}

func TestScriptInput_NoReposNoIdentity_BothFieldsEmpty(t *testing.T) {
	p := &Provisioner{Cfg: schema.Config{}}
	in := p.scriptInput()
	assert.Equal(t, "", in.GitCredentials, "no repos, no identity ⇒ no credentials")
	assert.Equal(t, "", in.GitConfig, "no repos, no identity ⇒ no gitconfig")
}

func TestScriptInput_GitConfigCarriesIdentityWhenReposDeclared(t *testing.T) {
	dir := makeRepoWithIdentity(t, "Fixture User", "fixture@example.com")
	url := "https://github.com/mdubb86/sewtrue.git"
	p := &Provisioner{
		MacCwd: dir,
		Cfg: schema.Config{
			Repos: map[string]schema.RepoConfig{
				"workspace": {URL: &url, Secret: "gh_token"},
			},
		},
	}
	in := p.scriptInput()
	assert.Contains(t, in.GitConfig, "[user]\n    name = Fixture User\n    email = fixture@example.com\n")
}
