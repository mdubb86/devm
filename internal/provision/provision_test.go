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
// every invocation, EXCEPT the first-boot marker probe (`test -f
// /var/lib/devm/provisioned`), which it reports absent — keeping
// callers on the first-boot path so gated steps still run. PATH is
// prepended.
func writeFakeTartOK(t *testing.T, dir string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"test -f %s"*) exit 1 ;;
esac
echo fake-tart-output
exit 0
`, provisionedMarker)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tart"), []byte(script), 0755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// writeFakeTartFailingAt drops a tart shell script that succeeds for
// every invocation EXCEPT those whose argv contains `failureMarker`,
// for which it exits 1. Like writeFakeTartOK, the first-boot marker
// probe is always reported absent so gated steps stay reachable.
func writeFakeTartFailingAt(t *testing.T, dir, failureMarker string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"test -f %s"*) exit 1 ;;
esac
for arg in "$@"; do
  case "$arg" in
    *%s*) echo "step failure marker matched: $arg" >&2; exit 1 ;;
  esac
done
exit 0
`, provisionedMarker, failureMarker)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tart"), []byte(script), 0755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// fakeRecordingTart records every argv sequence passed to ExecWithRetry.
type fakeRecordingTart struct {
	onExec func(argv []string) tart.ExecResult
}

func (f *fakeRecordingTart) Exec(ctx context.Context, name string, argv []string) tart.ExecResult {
	return f.onExec(argv)
}

func (f *fakeRecordingTart) ExecWithRetry(ctx context.Context, name string, argv []string) tart.ExecResult {
	return f.onExec(argv)
}

func (f *fakeRecordingTart) ExecStdin(ctx context.Context, name string, stdin io.Reader, argv []string) tart.ExecResult {
	return f.onExec(argv)
}

// isMarkerProbe reports whether argv is the first-boot marker read
// (`test -f /var/lib/devm/provisioned`) that Provisioner.markerExists issues.
func isMarkerProbe(argv []string) bool {
	return len(argv) == 3 && argv[0] == "test" && argv[1] == "-f" && argv[2] == provisionedMarker
}

func TestReloadBaseServices_EnablesSSH(t *testing.T) {
	// Fake tart that records every argv.
	var seen [][]string
	tr := &fakeRecordingTart{
		onExec: func(argv []string) tart.ExecResult {
			seen = append(seen, argv)
			return tart.ExecResult{ExitCode: 0}
		},
	}
	p := &Provisioner{Tart: tr, VMName: "vm"}
	var buf bytes.Buffer
	require.NoError(t, p.reloadBaseServices(context.Background(), &buf))

	// Must invoke both caddy reload AND ssh enable+start.
	joined := make([]string, 0, len(seen))
	for _, a := range seen {
		joined = append(joined, strings.Join(a, " "))
	}
	assert.Contains(t, joined, "sudo systemctl reload-or-restart caddy")
	found := false
	for _, s := range joined {
		if strings.Contains(s, "systemctl enable --now ssh") {
			found = true
			break
		}
	}
	assert.True(t, found, "reloadBaseServices must enable + start ssh; saw: %v", joined)
}

func TestApplySvcIngressFirewall_DockerWithDirectService_RunsScript(t *testing.T) {
	var seen [][]string
	tr := &fakeRecordingTart{
		onExec: func(argv []string) tart.ExecResult {
			seen = append(seen, argv)
			return tart.ExecResult{ExitCode: 0}
		},
	}
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Docker:  true,
		Services: map[string]schema.Service{
			"api": {Direct: true, Port: 54321},
		},
	}
	p := &Provisioner{Tart: tr, VMName: "vm", Cfg: cfg}
	var buf bytes.Buffer
	require.NoError(t, p.applySvcIngressFirewall(context.Background(), &buf))

	found := false
	for _, argv := range seen {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "ct original proto-dst 54321 accept") {
			found = true
		}
	}
	assert.True(t, found, "expected svc_ingress script with port 54321; saw: %v", seen)
}

func TestApplySvcIngressFirewall_NoDirectPorts_Skipped(t *testing.T) {
	cases := []struct {
		name string
		cfg  schema.Config
	}{
		{
			name: "docker false",
			cfg: schema.Config{
				Project: schema.Project{Name: "p"},
				Docker:  false,
				Services: map[string]schema.Service{
					"api": {Direct: true, Port: 54321},
				},
			},
		},
		{
			name: "docker true but no direct services",
			cfg: schema.Config{
				Project: schema.Project{Name: "p"},
				Docker:  true,
				Services: map[string]schema.Service{
					"api": {Port: 54321},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen [][]string
			tr := &fakeRecordingTart{
				onExec: func(argv []string) tart.ExecResult {
					seen = append(seen, argv)
					return tart.ExecResult{ExitCode: 0}
				},
			}
			p := &Provisioner{Tart: tr, VMName: "vm", Cfg: tc.cfg}
			var buf bytes.Buffer
			require.NoError(t, p.applySvcIngressFirewall(context.Background(), &buf))
			assert.Empty(t, seen, "no tart exec should run when there are no direct ports")
			assert.Contains(t, buf.String(), "skipping")
		})
	}
}

func TestProvisioner_RunsAllStepsOnHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFakeTartOK(t, dir)

	p := &Provisioner{
		Tart:            tart.New(),
		VMName:          "myproj-sbx",
		Cfg:             schema.Config{Project: schema.Project{Name: "myproj"}},
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
		"[step: install devm bundle]",
		"[step: reload base services]",
		"[step: apt-get update]",
		"[step: apt-get install packages]",
		"[step: run install commands]",
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

func TestProvisioner_FirstBootRunsGatedStepsAndWritesMarker(t *testing.T) {
	var ran []string
	fake := &fakeRecordingTart{onExec: func(argv []string) tart.ExecResult {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "test -f /var/lib/devm/provisioned") {
			return tart.ExecResult{ExitCode: 1} // absent → first boot
		}
		ran = append(ran, joined)
		return tart.ExecResult{ExitCode: 0}
	}}
	p := &Provisioner{Tart: fake, VMName: "vm", Cfg: schema.Config{Install: []string{"echo hi"}}}
	require.NoError(t, p.Run(context.Background(), &bytes.Buffer{}))

	joinedAll := strings.Join(ran, "\n")
	assert.Contains(t, joinedAll, "echo hi", "install: must run on first boot")
	assert.Contains(t, joinedAll, "touch /var/lib/devm/provisioned",
		"marker must be written after a successful first boot")
}

func TestProvisioner_RestartSkipsGatedSteps(t *testing.T) {
	var ran []string
	fake := &fakeRecordingTart{onExec: func(argv []string) tart.ExecResult {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "test -f /var/lib/devm/provisioned") {
			return tart.ExecResult{ExitCode: 0} // present → restart
		}
		ran = append(ran, joined)
		return tart.ExecResult{ExitCode: 0}
	}}
	p := &Provisioner{Tart: fake, VMName: "vm", Cfg: schema.Config{Install: []string{"echo hi"}}}
	require.NoError(t, p.Run(context.Background(), &bytes.Buffer{}))

	joinedAll := strings.Join(ran, "\n")
	assert.NotContains(t, joinedAll, "echo hi", "install: must NOT re-run on restart")
	assert.NotContains(t, joinedAll, "touch /var/lib/devm/provisioned",
		"marker must not be re-written when already present")
}

// TestProvisioner_MarkerWriteFailureIsPostInstall pins that a failure to
// write the first-boot marker — which only runs after every provisioning
// step, including "enable + start services" and "apply masks", has
// already succeeded — is classified as post-install. The VM is fully up
// and healthy at that point, so a one-off marker-write hiccup must leave
// it running rather than trigger teardown + recreate.
func TestProvisioner_MarkerWriteFailureIsPostInstall(t *testing.T) {
	fake := &fakeRecordingTart{onExec: func(argv []string) tart.ExecResult {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "test -f /var/lib/devm/provisioned") {
			return tart.ExecResult{ExitCode: 1} // absent → first boot
		}
		if strings.Contains(joined, "touch /var/lib/devm/provisioned") {
			return tart.ExecResult{ExitCode: 1} // marker write fails
		}
		return tart.ExecResult{ExitCode: 0}
	}}
	p := &Provisioner{Tart: fake, VMName: "vm", Cfg: schema.Config{Install: []string{"echo hi"}}}
	err := p.Run(context.Background(), &bytes.Buffer{})
	require.Error(t, err)
	assert.True(t, IsPostInstallFailure(err),
		"marker-write failure must be classified post-install so the VM is left running")
}

// TestProvisioner_SvcIngressRunsAfterEgressEnforcement pins the step
// order between the two firewall steps: svc_ingress rules must be
// applied only after egress enforcement has scaffolded/locked down
// the base chains, never before.
func TestProvisioner_SvcIngressRunsAfterEgressEnforcement(t *testing.T) {
	dir := t.TempDir()
	writeFakeTartOK(t, dir)

	p := &Provisioner{
		Tart:            tart.New(),
		VMName:          "myproj-sbx",
		Cfg:             schema.Config{Project: schema.Project{Name: "myproj"}},
		CARootPEM:       []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
		WorkspaceVMPath: "/Users/test/myproj",
	}
	var buf bytes.Buffer
	require.NoError(t, p.Run(context.Background(), &buf))
	out := buf.String()

	egressIdx := strings.Index(out, "[step: apply egress enforcement]")
	svcIngressIdx := strings.Index(out, "[step: apply svc_ingress firewall]")
	require.GreaterOrEqual(t, egressIdx, 0, "missing egress enforcement step header")
	require.GreaterOrEqual(t, svcIngressIdx, 0, "missing svc_ingress firewall step header")
	assert.Greater(t, svcIngressIdx, egressIdx, "svc_ingress firewall must run after egress enforcement")
}

// TestProvisioner_BootEnforcement_StartupMasksNftables pins that a
// project with startup: commands hands boot-restore ownership to
// devm-enforce.service/devm-startup.service — nftables.service must be
// masked so it can't race the startup: commands with enforcement already
// applied.
func TestProvisioner_BootEnforcement_StartupMasksNftables(t *testing.T) {
	var seen [][]string
	tr := &fakeRecordingTart{onExec: func(argv []string) tart.ExecResult {
		if !isMarkerProbe(argv) {
			seen = append(seen, argv)
		}
		return tart.ExecResult{ExitCode: 0}
	}}
	p := &Provisioner{
		Tart: tr, VMName: "vm",
		Cfg: schema.Config{Startup: []string{"echo hi"}},
	}
	var buf bytes.Buffer
	require.NoError(t, p.setupBootEnforcement(context.Background(), &buf))

	joined := strings.Join(flattenArgvs(seen), "\n")
	assert.Contains(t, joined, "mask nftables")
	assert.Contains(t, joined, "enable")
	assert.Contains(t, joined, "devm-enforce.service")
	assert.NotContains(t, joined, "enable --now nftables")
}

// TestProvisioner_BootEnforcement_NoStartupEnablesNftables pins that a
// project with no startup: commands keeps the stock nftables.service as
// the boot-restore path (and unmasks it, in case a previous project
// revision had startup: and masked it).
func TestProvisioner_BootEnforcement_NoStartupEnablesNftables(t *testing.T) {
	var seen [][]string
	tr := &fakeRecordingTart{onExec: func(argv []string) tart.ExecResult {
		if !isMarkerProbe(argv) {
			seen = append(seen, argv)
		}
		return tart.ExecResult{ExitCode: 0}
	}}
	p := &Provisioner{Tart: tr, VMName: "vm", Cfg: schema.Config{}}
	var buf bytes.Buffer
	require.NoError(t, p.setupBootEnforcement(context.Background(), &buf))

	joined := strings.Join(flattenArgvs(seen), "\n")
	assert.Contains(t, joined, "unmask nftables.service")
	assert.Contains(t, joined, "enable nftables.service")
	// "unmask" is not "mask": guard against the substring false-positive.
	assert.NotContains(t, joined, "systemctl mask nftables")
	assert.NotContains(t, joined, "devm-enforce.service")
	assert.NotContains(t, joined, "devm-startup.service")
}

// TestProvisioner_RunStartup_FirstBootOnly pins that devm-startup.service
// is started explicitly only on first boot (systemd already ran it at
// boot on a restart) and only when the project declares startup:.
func TestProvisioner_RunStartup_FirstBootOnly(t *testing.T) {
	t.Run("first boot with startup commands starts the service", func(t *testing.T) {
		var seen [][]string
		fake := &fakeRecordingTart{onExec: func(argv []string) tart.ExecResult {
			if isMarkerProbe(argv) {
				return tart.ExecResult{ExitCode: 1} // absent -> first boot
			}
			seen = append(seen, argv)
			return tart.ExecResult{ExitCode: 0}
		}}
		p := &Provisioner{Tart: fake, VMName: "vm", Cfg: schema.Config{Startup: []string{"echo hi"}}}
		p.firstBoot = !p.markerExists(context.Background())
		var buf bytes.Buffer
		require.NoError(t, p.runStartupCommands(context.Background(), &buf))

		joined := strings.Join(flattenArgvs(seen), "\n")
		assert.Contains(t, joined, "start devm-startup.service")
	})

	t.Run("restart does not start the service", func(t *testing.T) {
		var seen [][]string
		fake := &fakeRecordingTart{onExec: func(argv []string) tart.ExecResult {
			if isMarkerProbe(argv) {
				return tart.ExecResult{ExitCode: 0} // present -> restart
			}
			seen = append(seen, argv)
			return tart.ExecResult{ExitCode: 0}
		}}
		p := &Provisioner{Tart: fake, VMName: "vm", Cfg: schema.Config{Startup: []string{"echo hi"}}}
		require.NoError(t, p.Run(context.Background(), &bytes.Buffer{}))

		joined := strings.Join(flattenArgvs(seen), "\n")
		assert.NotContains(t, joined, "start devm-startup.service")
	})

	t.Run("no startup commands never starts the service", func(t *testing.T) {
		var seen [][]string
		fake := &fakeRecordingTart{onExec: func(argv []string) tart.ExecResult {
			if isMarkerProbe(argv) {
				return tart.ExecResult{ExitCode: 1} // absent -> first boot
			}
			seen = append(seen, argv)
			return tart.ExecResult{ExitCode: 0}
		}}
		p := &Provisioner{Tart: fake, VMName: "vm", Cfg: schema.Config{}}
		require.NoError(t, p.Run(context.Background(), &bytes.Buffer{}))

		joined := strings.Join(flattenArgvs(seen), "\n")
		assert.NotContains(t, joined, "devm-startup.service")
	})
}

// flattenArgvs joins each recorded argv into a single space-joined string
// per invocation, for substring assertions across the whole recording.
func flattenArgvs(argvs [][]string) []string {
	out := make([]string, 0, len(argvs))
	for _, a := range argvs {
		out = append(out, strings.Join(a, " "))
	}
	return out
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
			Project:  schema.Project{Name: "myproj"},
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
	assert.NotContains(t, out, "[step: systemctl daemon-reload]")
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
			Project: schema.Project{Name: "myproj"},
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
		Project: schema.Project{Name: "p"},
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
	if isMarkerProbe(argv) {
		// Always report the first-boot marker absent so gated steps
		// (e.g. the install step under test) still run.
		return tart.ExecResult{ExitCode: 1}
	}
	if dl, ok := ctx.Deadline(); ok {
		d.deadlines = append(d.deadlines, time.Until(dl))
		d.argvs = append(d.argvs, append([]string(nil), argv...))
	}
	return tart.ExecResult{ExitCode: 0}
}

func (d *deadlineCapturingTart) ExecWithRetry(ctx context.Context, name string, argv []string) tart.ExecResult {
	return d.Exec(ctx, name, argv)
}

func (d *deadlineCapturingTart) ExecStdin(ctx context.Context, name string, _ io.Reader, argv []string) tart.ExecResult {
	return d.Exec(ctx, name, argv)
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

func (s *slowTart) Exec(ctx context.Context, _ string, argv []string) tart.ExecResult {
	if isMarkerProbe(argv) {
		// Always report the first-boot marker absent so the install
		// step under test still runs (and can time out).
		return tart.ExecResult{ExitCode: 1}
	}
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

func (s *slowTart) ExecWithRetry(ctx context.Context, name string, argv []string) tart.ExecResult {
	return s.Exec(ctx, name, argv)
}

func (s *slowTart) ExecStdin(ctx context.Context, name string, _ io.Reader, argv []string) tart.ExecResult {
	return s.Exec(ctx, name, argv)
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
				Project: schema.Project{Name: "p"},
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
		Project: schema.Project{Name: "p"},
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

func (a *argvRecordingTart) ExecWithRetry(ctx context.Context, name string, argv []string) tart.ExecResult {
	return a.Exec(ctx, name, argv)
}

func (a *argvRecordingTart) ExecStdin(ctx context.Context, name string, _ io.Reader, argv []string) tart.ExecResult {
	return a.Exec(ctx, name, argv)
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
				Project: schema.Project{Name: "p"},
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
		Project: schema.Project{Name: "p"},
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
		[]string{"/opt/devm/scripts/with-devm-env", "bash", "-e", "-o", "pipefail", "-c", "true"},
		got,
	)
}

func TestProvisioner_InstallStepTimeout_ErrorMessage(t *testing.T) {
	// Step exceeds the deadline → structured error names the step
	// number and the command that timed out.
	t.Setenv("DEVM_INSTALL_STEP_TIMEOUT_S", "1")
	fakeTart := newSlowTart(2 * time.Second)
	cfg := schema.Config{
		Project: schema.Project{Name: "p"},
		Install: []string{"sleep 2"},
	}
	p := &Provisioner{Tart: fakeTart, VMName: "p-vm", Cfg: cfg}
	err := p.Run(context.Background(), io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `install step 1`)
	assert.Contains(t, err.Error(), `sleep 2`)
	assert.Contains(t, err.Error(), "timed out")
}
