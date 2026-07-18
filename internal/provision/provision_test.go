package provision

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStreamTart is a tartExecer that answers the first-boot marker probe
// from markerPresent and records the single ExecStream call the rewritten
// Run makes, optionally emitting scripted output lines and a scripted exit
// code / error.
type fakeStreamTart struct {
	markerPresent bool

	streamCalls int
	lastArgv    []string
	lastStdin   []byte

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
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.lastStdin = b
	}
	if f.emit != nil {
		f.emit(onLine)
	}
	return f.exit, f.streamErr
}

// scriptOf returns the composed guest script from the recorded ExecStream
// argv (`bash -c <script>`).
func scriptOf(t *testing.T, f *fakeStreamTart) string {
	t.Helper()
	require.Len(t, f.lastArgv, 3, "ExecStream argv must be [bash -c <script>]")
	assert.Equal(t, "bash", f.lastArgv[0])
	assert.Equal(t, "-c", f.lastArgv[1])
	return f.lastArgv[2]
}

func baseProvisioner(f *fakeStreamTart, cfg schema.Config) *Provisioner {
	return &Provisioner{
		Tart:            f,
		VMName:          "myproj-sbx",
		Cfg:             cfg,
		CARootPEM:       []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
		WorkspaceVMPath: "/Users/test/myproj",
		EnforcedNft:     "table inet devm_filter { policy drop }",
	}
}

func TestRun_ShipsExactlyOneExecStreamWithScriptAndTar(t *testing.T) {
	f := &fakeStreamTart{} // marker absent → first boot
	p := baseProvisioner(f, schema.Config{
		Project:  schema.Project{Name: "myproj"},
		Packages: []string{"jq"},
		Install:  []string{"echo hi"},
	})
	var buf bytes.Buffer
	require.NoError(t, p.Run(context.Background(), &buf, nil))

	require.Equal(t, 1, f.streamCalls, "Run must ship exactly ONE ExecStream")
	script := scriptOf(t, f)

	// The composed script fail-fasts, opens the window, runs first-boot
	// work, enforces, and activates the target last.
	assert.Contains(t, script, "set -eo pipefail")
	assert.Contains(t, script, "sudo apt-get install -y 'jq'")
	assert.Contains(t, script, "/opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo hi'")
	assert.Contains(t, script, "systemctl start devm.target")
	// First-boot completion marker is written by the script.
	assert.Contains(t, script, "touch /var/lib/devm/provisioned")

	// Stdin is the bundle tar; it must be a valid archive carrying the
	// devm-owned artifacts (install.sh + startup.sh).
	require.NotEmpty(t, f.lastStdin, "ExecStream stdin must carry the bundle tar")
	names := tarEntryNames(t, f.lastStdin)
	assert.Contains(t, names, "install.sh")
	assert.Contains(t, names, "startup.sh")
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

func TestRun_ForwardsStreamedLinesToWriterAndOnLine(t *testing.T) {
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
	require.NoError(t, p.Run(context.Background(), &buf, func(stream, line string) {
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

func TestRun_NonZeroExitClassifiesFailureByStage(t *testing.T) {
	tests := []struct {
		name         string
		failAtStage  string
		wantPostInst bool
	}{
		{"apt/install phase tears down", "install", false},
		{"docker phase tears down", "docker", false},
		{"enforce phase tears down", "enforce", false},
		{"templates phase tears down (runs pre-enforce, unenforced)", "templates", false},
		{"service phase keeps vm", "services", true},
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
			err := p.Run(context.Background(), io.Discard, nil)
			require.Error(t, err)

			var sf *StepFailure
			require.ErrorAs(t, err, &sf)
			assert.Equal(t, stage, sf.Step, "failure must be tagged with the stage it reached")
			assert.Equal(t, tc.wantPostInst, IsPostInstallFailure(err))
		})
	}
}

func TestRun_ExecStreamTransportErrorIsStepFailure(t *testing.T) {
	f := &fakeStreamTart{streamErr: context.DeadlineExceeded}
	p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})
	err := p.Run(context.Background(), io.Discard, nil)
	require.Error(t, err)
	var sf *StepFailure
	require.ErrorAs(t, err, &sf)
}

func TestRun_RestartOmitsFirstBootWork(t *testing.T) {
	f := &fakeStreamTart{markerPresent: true} // present → restart, not first boot
	p := baseProvisioner(f, schema.Config{
		Project:  schema.Project{Name: "myproj"},
		Packages: []string{"jq"},
		Install:  []string{"echo hi"},
	})
	require.NoError(t, p.Run(context.Background(), io.Discard, nil))

	script := scriptOf(t, f)
	// First-boot-only work must NOT appear on a restart.
	assert.NotContains(t, script, "apt-get install")
	assert.NotContains(t, script, "echo hi")
	assert.NotContains(t, script, "::devm:stage:packages::")
	// And the completion marker is not re-written.
	assert.NotContains(t, script, "touch /var/lib/devm/provisioned")
	// Enforcement + target still run every boot.
	assert.Contains(t, script, "EnforcedNft-applied-marker")
	assert.Contains(t, script, "systemctl start devm.target")
	// The guest-nft flush is unconditional — a restart with no first-boot
	// or open-stage work still needs the base image's policy-drop lock
	// cleared, or it would drop softnet's own egress.
	assert.Contains(t, script, "sudo nft flush ruleset")
}

func TestRun_EnforcedNftBakedIntoScript(t *testing.T) {
	f := &fakeStreamTart{}
	p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})
	p.EnforcedNft = "table inet devm_filter { chain output { policy drop } }"
	require.NoError(t, p.Run(context.Background(), io.Discard, nil))

	script := scriptOf(t, f)
	assert.Contains(t, script, "table inet devm_filter { chain output { policy drop } }")
	// Applied via nft before the target.
	assert.Less(t, strings.Index(script, "DEVM_ENFORCE_NFT"),
		strings.Index(script, "systemctl start devm.target"))
}

// TestRun_DnsmasqTimesyncdBakedIntoScript pins that the daemon-supplied
// dnsmasq/timesyncd config (fetched via serviceapi.Client.EnforcementConfig
// and set on Provisioner by orchestrator.RunShell) flows through
// scriptInput into the composed script's enforce phase — the runtime
// DNS/NTP fix the boot-integrity-gate rewrite had dropped.
func TestRun_DnsmasqTimesyncdBakedIntoScript(t *testing.T) {
	f := &fakeStreamTart{}
	p := baseProvisioner(f, schema.Config{Project: schema.Project{Name: "myproj"}})
	p.DnsmasqScript = "sudo tee /etc/dnsmasq.d/devm.conf > /dev/null <<'DEVM_DNSMASQ'\nserver=192.168.64.1#53101\nDEVM_DNSMASQ\n"
	p.TimesyncdScript = "sudo tee /etc/systemd/timesyncd.conf.d/devm.conf > /dev/null <<'DEVM_TIMESYNCD'\nNTP=192.0.2.1\nDEVM_TIMESYNCD\n"
	require.NoError(t, p.Run(context.Background(), io.Discard, nil))

	script := scriptOf(t, f)
	assert.Contains(t, script, "/etc/dnsmasq.d/devm.conf")
	assert.Contains(t, script, "server=192.168.64.1#53101")
	assert.Contains(t, script, "/etc/systemd/timesyncd.conf.d/devm.conf")
	assert.Contains(t, script, "NTP=192.0.2.1")
	// applied in the enforce phase — after the enforced nft ruleset,
	// before services/target come up.
	enforceIdx := strings.Index(script, "EnforcedNft-applied-marker")
	require.Greater(t, enforceIdx, 0)
	assert.Greater(t, strings.Index(script, "/etc/dnsmasq.d/devm.conf"), enforceIdx)
	assert.Greater(t, strings.Index(script, "/etc/systemd/timesyncd.conf.d/devm.conf"), enforceIdx)
	assert.Less(t, strings.Index(script, "/etc/dnsmasq.d/devm.conf"),
		strings.Index(script, "systemctl start devm.target"))
}

func TestRun_RoutingOnlyServiceOmittedButProcessServicesStarted(t *testing.T) {
	f := &fakeStreamTart{}
	p := baseProvisioner(f, schema.Config{
		Project: schema.Project{Name: "myproj"},
		Services: map[string]schema.Service{
			"routing-only": {Hostname: "x.test", Port: 8080},
			"with-exec":    {Exec: []string{"/bin/true"}},
		},
	})
	require.NoError(t, p.Run(context.Background(), io.Discard, nil))

	script := scriptOf(t, f)
	assert.Contains(t, script, "systemctl start with-exec.service")
	assert.NotContains(t, script, "routing-only.service")
}

func TestRun_MaskChownedToServiceUserBeforeMount(t *testing.T) {
	tests := []struct {
		name      string
		svcUser   string
		wantOwner string
	}{
		{"default user is devm", "", "devm"},
		{"explicit user", "e2euser", "e2euser"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStreamTart{}
			p := baseProvisioner(f, schema.Config{
				Project: schema.Project{Name: "p"},
				Services: map[string]schema.Service{
					"svc": {
						Exec:  []string{"/bin/true"},
						User:  tc.svcUser,
						Masks: []schema.Mask{{Path: "data", Size: "10m"}},
					},
				},
			})
			p.WorkspaceVMPath = "/Users/x/proj"
			require.NoError(t, p.Run(context.Background(), io.Discard, nil))

			script := scriptOf(t, f)
			chown := "chown " + tc.wantOwner + " '/var/devm/masks/p/svc/data'"
			assert.Contains(t, script, chown)
			// chown must precede the bind mount, or the mount covers the target.
			chownIdx := strings.Index(script, chown)
			mountIdx := strings.Index(script, "mount --bind '/var/devm/masks/p/svc/data'")
			require.Greater(t, chownIdx, 0)
			assert.Greater(t, mountIdx, chownIdx)
		})
	}
}

func TestRun_TemplatesTriggerDispatcher(t *testing.T) {
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
	require.NoError(t, p.Run(context.Background(), io.Discard, nil))
	assert.Contains(t, scriptOf(t, f), "install-templates.sh")
}
