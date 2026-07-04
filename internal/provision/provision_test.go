package provision

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeTartOK drops a tart shell script into dir that succeeds for
// every invocation. PATH is prepended.
func writeFakeTartOK(t *testing.T, dir string) {
	t.Helper()
	script := "#!/bin/sh\necho fake-tart-output\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tart"), []byte(script), 0755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// writeFakeTartFailingAt drops a tart shell script that succeeds for
// every invocation EXCEPT those whose argv contains `failureMarker`,
// for which it exits 1.
func writeFakeTartFailingAt(t *testing.T, dir, failureMarker string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    *%s*) echo "step failure marker matched: $arg" >&2; exit 1 ;;
  esac
done
exit 0
`, failureMarker)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tart"), []byte(script), 0755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestProvisioner_RunsAllStepsOnHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFakeTartOK(t, dir)

	p := &Provisioner{
		Tart:            tart.New(),
		VMName:          "myproj-sbx",
		Cfg:             schema.Config{Project: schema.Project{ID: "myproj", VMName: "myproj-vm"}},
		CARootPEM:       []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
		WorkspaceVMPath: "/Users/test/myproj",
	}
	var buf bytes.Buffer
	err := p.Run(context.Background(), &buf)
	require.NoError(t, err)
	out := buf.String()

	// All expected step headers appear in order.
	expectedSteps := []string{
		"[step: mkdir workspace parents]",
		"[step: install CA root]",
		"[step: write Caddyfile]",
		"[step: write dnsmasq config]",
		"[step: reload base services]",
		"[step: apt-get update]",
		"[step: apt-get install packages]",
		"[step: run install commands]",
		"[step: install service units]",
		"[step: systemctl daemon-reload]",
		"[step: enable + start services]",
		"[step: apply masks]",
	}
	prev := -1
	for _, marker := range expectedSteps {
		idx := strings.Index(out, marker)
		require.GreaterOrEqual(t, idx, 0, "missing step header: %s\nfull output:\n%s", marker, out)
		assert.Greater(t, idx, prev, "step %s out of order", marker)
		prev = idx
	}
}

// Note: failure-isolation testing (verifying that later steps DON'T
// run after an earlier one fails) is harder because the fake tart
// would need to track invocations. Acceptable simplification: verify
// the error propagation behavior only.
func TestProvisioner_FailsFastOnTartError(t *testing.T) {
	dir := t.TempDir()
	// Fail when argv contains "apt-get" — i.e., the apt update step.
	writeFakeTartFailingAt(t, dir, "apt-get")

	p := &Provisioner{
		Tart:   tart.New(),
		VMName: "myproj-sbx",
		Cfg: schema.Config{
			Project:  schema.Project{ID: "myproj", VMName: "myproj-vm"},
			Packages: []string{"jq"}, // forces apt-get update to actually run
		},
		CARootPEM:       []byte("fake-pem\n"),
		WorkspaceVMPath: "/Users/test/myproj",
	}
	var buf bytes.Buffer
	err := p.Run(context.Background(), &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apt-get update")

	out := buf.String()
	// Steps after the failure must NOT appear.
	assert.NotContains(t, out, "[step: install service units]")
	assert.NotContains(t, out, "[step: enable + start services]")
}

// writeFakeTartIsActiveMap writes a tart shell script that responds to
// `systemctl is-active <name>` probes using the given states map (names
// absent from the map return "active" / exit-0). All other commands
// succeed silently.
func writeFakeTartIsActiveMap(t *testing.T, dir string, states map[string]string) {
	t.Helper()
	var cases strings.Builder
	for name, state := range states {
		exitCode := 0
		if state != "active" {
			exitCode = 1
		}
		fmt.Fprintf(&cases, "        %s) echo %s; exit %d ;;\n", name, state, exitCode)
	}
	script := fmt.Sprintf(`#!/bin/sh
prev=""
for arg in "$@"; do
    if [ "$prev" = "is-active" ]; then
        case "$arg" in
%s        *) echo active; exit 0 ;;
        esac
    fi
    prev="$arg"
done
echo fake-tart-output
exit 0
`, cases.String())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tart"), []byte(script), 0755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestProvisioner_RoutingOnlyServiceSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFakeTartOK(t, dir)

	p := &Provisioner{
		Tart:   tart.New(),
		VMName: "myproj-sbx",
		Cfg: schema.Config{
			Project: schema.Project{ID: "myproj", VMName: "myproj-vm"},
			Services: map[string]schema.Service{
				// Routing-only (no Exec, no Systemd).
				"routing-only": {Hostname: "x.test", Port: 8080},
				// Has Exec, should get systemctl enable --now.
				"with-exec": {Exec: []string{"/bin/true"}},
			},
		},
		CARootPEM:       []byte("fake\n"),
		WorkspaceVMPath: "/Users/test/myproj",
	}
	var buf bytes.Buffer
	require.NoError(t, p.Run(context.Background(), &buf))

	out := buf.String()
	assert.Contains(t, out, "(skip routing-only — routing-only declaration)")
}

func TestProvisioner_AssertsServicesActive(t *testing.T) {
	// Service health check: after `systemctl enable --now <unit>`, the
	// provisioner polls `systemctl is-active <unit>` (bounded by a short
	// wait) and returns a structured error if any unit ends in "failed".
	dir := t.TempDir()
	writeFakeTartIsActiveMap(t, dir, map[string]string{"broken": "failed"})

	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Services: map[string]schema.Service{
			"broken": {Systemd: "[Service]\nExecStart=/bin/false\n"},
		},
	}
	p := &Provisioner{
		Tart:            tart.New(),
		VMName:          "p-vm",
		Cfg:             cfg,
		CARootPEM:       []byte("fake\n"),
		WorkspaceVMPath: "/tmp/p",
	}
	err := p.Run(context.Background(), io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), `service "broken" did not become active`)
	require.Contains(t, err.Error(), "status=failed")
}

// deadlineCapturingTart records the remaining deadline duration and the
// argv of every context passed to Exec. Used to verify that install steps
// run under the correct per-step timeout budget and go through the
// with-devm-env wrapper.
type deadlineCapturingTart struct {
	deadlines []time.Duration
	argvs     [][]string
}

func (d *deadlineCapturingTart) Exec(ctx context.Context, _ string, argv []string) tart.ExecResult {
	if dl, ok := ctx.Deadline(); ok {
		d.deadlines = append(d.deadlines, time.Until(dl))
		d.argvs = append(d.argvs, append([]string(nil), argv...))
	}
	return tart.ExecResult{ExitCode: 0}
}

func newDeadlineCapturingTart() *deadlineCapturingTart {
	return &deadlineCapturingTart{}
}

// slowTart blocks Exec calls that carry a deadline context for `delay`,
// simulating a command that hangs longer than its per-step timeout budget.
// Calls without a deadline (non-install provisioner steps) return immediately
// so the test only burns time on the step under test.
type slowTart struct {
	delay time.Duration
}

func (s *slowTart) Exec(ctx context.Context, _ string, _ []string) tart.ExecResult {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		return tart.ExecResult{ExitCode: 0}
	}
	select {
	case <-ctx.Done():
		return tart.ExecResult{ExitCode: -1}
	case <-time.After(s.delay):
		return tart.ExecResult{ExitCode: 0}
	}
}

func newSlowTart(d time.Duration) *slowTart {
	return &slowTart{delay: d}
}

func TestProvisioner_InstallStepTimeout_DefaultAndOverride(t *testing.T) {
	// The env var must be readable from os.Getenv at run time, and a
	// small override value must be honored. The test exercises both
	// paths by spying on the deadline passed into tart.Exec.

	tests := []struct {
		name     string
		envVal   string
		wantSecs int
	}{
		{name: "default 600s when env unset", envVal: "", wantSecs: 600},
		{name: "env override 3s honored", envVal: "3", wantSecs: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envVal == "" {
				t.Setenv("DEVM_INSTALL_STEP_TIMEOUT_S", "")
			} else {
				t.Setenv("DEVM_INSTALL_STEP_TIMEOUT_S", tc.envVal)
			}
			fakeTart := newDeadlineCapturingTart()
			cfg := schema.Config{
				Project: schema.Project{ID: "p", VMName: "p-vm"},
				Install: []string{"echo hello"},
			}
			p := &Provisioner{Tart: fakeTart, VMName: "p-vm", Cfg: cfg}
			_ = p.Run(context.Background(), io.Discard)
			require.NotEmpty(t, fakeTart.deadlines)
			// Allow a 100ms wiggle for scheduling.
			got := fakeTart.deadlines[0]
			assert.InDelta(t, tc.wantSecs, got.Seconds(), 0.2,
				"install step deadline mismatch")
		})
	}
}

func TestProvisioner_OneShotServiceInactiveIsSuccess(t *testing.T) {
	// A service declared with `restart: no` ran-to-completion means
	// systemctl reports `inactive` — not `active`. The health check must
	// treat that as success, not a failure.
	dir := t.TempDir()
	writeFakeTartIsActiveMap(t, dir, map[string]string{"oneshot": "inactive"})

	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Services: map[string]schema.Service{
			"oneshot": {
				Exec:    []string{"/bin/true"},
				Restart: "no",
			},
		},
	}
	p := &Provisioner{
		Tart:            tart.New(),
		VMName:          "p-vm",
		Cfg:             cfg,
		CARootPEM:       []byte("fake\n"),
		WorkspaceVMPath: "/tmp/p",
	}
	require.NoError(t, p.Run(context.Background(), io.Discard))
}

// argvRecordingTart records every argv slice passed to Exec so callers
// can assert on what the provisioner asked tart to run.
type argvRecordingTart struct{ argvs [][]string }

func (a *argvRecordingTart) Exec(_ context.Context, _ string, argv []string) tart.ExecResult {
	a.argvs = append(a.argvs, append([]string(nil), argv...))
	return tart.ExecResult{ExitCode: 0}
}

func TestProvisioner_ApplyMasks_ChownsToServiceUser(t *testing.T) {
	// Bug fix: applyMasks was `sudo mkdir`-ing the mask dir and NEVER
	// chowning it, so a non-root service couldn't write into its own
	// mask. Pin: the emitted bash script chowns the mask dir to the
	// service's User (default devm).
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
			rec := &argvRecordingTart{}
			cfg := schema.Config{
				Project: schema.Project{ID: "p", VMName: "p-vm"},
				Services: map[string]schema.Service{
					"svc": {
						Exec:  []string{"/bin/true"},
						User:  tc.svcUser,
						Masks: []schema.Mask{{Path: "data", Size: "10m"}},
					},
				},
			}
			p := &Provisioner{
				Tart:            rec,
				VMName:          "p-vm",
				Cfg:             cfg,
				CARootPEM:       []byte("fake\n"),
				WorkspaceVMPath: "/Users/x/proj",
			}
			require.NoError(t, p.Run(context.Background(), io.Discard))

			var maskScript string
			for _, argv := range rec.argvs {
				// applyMasks goes through execShell → `bash -e -o pipefail -c "<script>"`.
				if len(argv) >= 5 && argv[0] == "bash" && argv[len(argv)-2] == "-c" &&
					strings.Contains(argv[len(argv)-1], "/var/devm/masks") {
					maskScript = argv[len(argv)-1]
					break
				}
			}
			require.NotEmpty(t, maskScript, "no mask-install bash invocation captured")
			assert.Contains(t, maskScript,
				fmt.Sprintf("sudo chown %s /var/devm/masks/p/svc/data", tc.wantOwner),
				"mask script must chown the mask dir to the service's User (default devm)")
			// Order matters: chown before bind mount, otherwise the mount
			// covers up the chown target.
			chownIdx := strings.Index(maskScript, "chown")
			mountIdx := strings.Index(maskScript, "mount --bind")
			assert.True(t, chownIdx > 0 && chownIdx < mountIdx,
				"chown must precede mount --bind in the mask script; got:\n%s", maskScript)
		})
	}
}

func TestProvisioner_InstallStepsGoThroughWithDevmEnvWrapper(t *testing.T) {
	// Pin: install commands run via
	//   with-devm-env bash -e -o pipefail -c <cmd>
	// so .devm/.env is sourced (WORKSPACE_DIR, path: entries, cfg.Env).
	// Regression pin for Bug L.
	fakeTart := newDeadlineCapturingTart()
	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Install: []string{"true"},
	}
	p := &Provisioner{
		Tart: fakeTart, VMName: "p-vm", Cfg: cfg,
		WorkspaceVMPath: "/Users/x/repo",
	}
	_ = p.Run(context.Background(), io.Discard)
	require.Len(t, fakeTart.argvs, 1, "expected one deadline-carrying call (the install step)")
	got := fakeTart.argvs[0]
	require.Equal(t,
		[]string{"/Users/x/repo/.devm/scripts/with-devm-env", "bash", "-e", "-o", "pipefail", "-c", "true"},
		got,
	)
}

func TestProvisioner_InstallStepTimeout_ErrorMessage(t *testing.T) {
	// Step exceeds the deadline → structured error names the step
	// number and the command that timed out.
	t.Setenv("DEVM_INSTALL_STEP_TIMEOUT_S", "1")
	fakeTart := newSlowTart(2 * time.Second)
	cfg := schema.Config{
		Project: schema.Project{ID: "p", VMName: "p-vm"},
		Install: []string{"sleep 2"},
	}
	p := &Provisioner{Tart: fakeTart, VMName: "p-vm", Cfg: cfg}
	err := p.Run(context.Background(), io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `install step 1`)
	assert.Contains(t, err.Error(), `sleep 2`)
	assert.Contains(t, err.Error(), "timed out")
}
