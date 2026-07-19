package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- fakes for RunShell tests ----------

// fakeVMAdmin is a fake VMAdminClient for unit-testing RunShell.
type fakeVMAdmin struct {
	mu                    sync.Mutex
	statusResp            serviceapi.VMStatusResponse
	statusErr             error
	startCalled           int
	startErr              error
	startProjectIP        string
	stopCalled            int
	stopErr               error
	applyIronProxyCalled  int
	applyIronProxyReq     serviceapi.VMApplyIronProxyRequest // last request received, for assertions
	applyIronProxyResp    serviceapi.VMApplyIronProxyResponse
	applyIronProxyRespSet bool // distinguishes an explicit all-false resp from "unset"
	applyIronProxyErr     error

	openEgressCalled             int
	openEgressErr                error
	applyEgressEnforcementCalled int
	applyEgressEnforcementErr    error
	// callOrder records the relative order OpenEgress/ApplyEgressEnforcement
	// were invoked in, for asserting they bracket the RunOpen/RunEnforced
	// execs — entries append "open-egress" / "apply-egress-enforcement".
	callOrder []string
	// logPath, when set, also appends the same markers into a shared file
	// that the fake tart binary writes its own invocations into, so a test
	// can read one ordered timeline across both fakes.
	logPath string
}

func (f *fakeVMAdmin) VMStatus(_ context.Context, _ string) (serviceapi.VMStatusResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statusResp, f.statusErr
}

func (f *fakeVMAdmin) StartVM(_ context.Context, _ serviceapi.VMStartRequest) (serviceapi.VMStartResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalled++
	if f.startErr != nil {
		return serviceapi.VMStartResponse{}, f.startErr
	}
	return serviceapi.VMStartResponse{ProjectIP: f.startProjectIP}, nil
}

func (f *fakeVMAdmin) EnforcementConfig(_ context.Context, _ string) (serviceapi.VMEnforcementConfigResponse, error) {
	return serviceapi.VMEnforcementConfigResponse{
		TimesyncdScript: "sudo tee /etc/systemd/timesyncd.conf.d/devm.conf > /dev/null <<'DEVM_TIMESYNCD'\nDEVM_TIMESYNCD\n",
	}, nil
}

func (f *fakeVMAdmin) StopVM(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalled++
	return f.stopErr
}

func (f *fakeVMAdmin) ApplyIronProxy(_ context.Context, req serviceapi.VMApplyIronProxyRequest) (serviceapi.VMApplyIronProxyResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyIronProxyCalled++
	f.applyIronProxyReq = req
	if f.applyIronProxyErr != nil {
		return serviceapi.VMApplyIronProxyResponse{}, f.applyIronProxyErr
	}
	if f.applyIronProxyRespSet {
		return f.applyIronProxyResp, nil
	}
	// Default: behaves like a successful revive so existing
	// adopt-in-place tests that don't care about this call still
	// pass through provisionAndAttach.
	return serviceapi.VMApplyIronProxyResponse{Applied: true, VMRunning: true}, nil
}

func (f *fakeVMAdmin) OpenEgress(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openEgressCalled++
	f.callOrder = append(f.callOrder, "open-egress")
	f.appendLog("OPEN-EGRESS")
	return f.openEgressErr
}

func (f *fakeVMAdmin) ApplyEgressEnforcement(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applyEgressEnforcementCalled++
	f.callOrder = append(f.callOrder, "apply-egress-enforcement")
	f.appendLog("APPLY-EGRESS-ENFORCEMENT")
	return f.applyEgressEnforcementErr
}

// appendLog writes a marker line into f.logPath, if set, interleaving with
// the fake tart binary's own invocation log so a test can read one ordered
// timeline across both fakes. Caller must hold f.mu.
func (f *fakeVMAdmin) appendLog(marker string) {
	if f.logPath == "" {
		return
	}
	fh, err := os.OpenFile(f.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	fmt.Fprintln(fh, marker)
}

// stubSpawner records Start calls and hands out scripted SpawnedCmds
// in FIFO order. If the queue is empty, returns a fresh stubCmd whose
// Wait will block forever.
type stubSpawner struct {
	mu       sync.Mutex
	started  [][]string
	cmds     []*stubCmd
	cmdQueue []*stubCmd
}

func (s *stubSpawner) Start(name string, args ...string) (SpawnedCmd, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = append(s.started, append([]string{name}, args...))
	if len(s.cmdQueue) > 0 {
		c := s.cmdQueue[0]
		s.cmdQueue = s.cmdQueue[1:]
		s.cmds = append(s.cmds, c)
		return c, nil
	}
	c := &stubCmd{waitErr: make(chan error, 1)}
	s.cmds = append(s.cmds, c)
	return c, nil
}

type stubCmd struct {
	waitErr chan error
	exitRC  int // exit code returned alongside waitErr
	killed  bool
	pid     int
}

func (c *stubCmd) Wait() (int, error) {
	err := <-c.waitErr
	return c.exitRC, err
}
func (c *stubCmd) Kill() error {
	c.killed = true
	c.exitRC = -1
	// Non-blocking send so Kill is idempotent / safe to call multiple times.
	select {
	case c.waitErr <- errors.New("killed"):
	default:
	}
	return nil
}
func (c *stubCmd) Pid() int { return c.pid }

func minimalCfg() schema.Config {
	return schema.Config{
		Project:  schema.Project{Name: "x"},
		Services: map[string]schema.Service{},
	}
}

// ---------- RunShell tests ----------

// TestRunShellWarmPath_AttachesWithoutStart verifies that when the VM is
// already running AND devm.target is active (fully provisioned), the
// daemon is NOT asked to start it again, no provisioning runs, the
// user shell is spawned via tart exec, and — Fix 2, defense-in-depth —
// ApplyEgressEnforcement is re-asserted before attach even though this VM
// should already be enforced from its own cold start.
func TestRunShellWarmPath_AttachesWithoutStart(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: true, Running: true},
	}
	tartBin, logPath := fakeTartBinState(t, repoRoot, true, false)

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	admin.mu.Lock()
	assert.Equal(t, 0, admin.startCalled, "StartVM must NOT be called on the warm path")
	assert.Equal(t, 1, admin.applyEgressEnforcementCalled,
		"warm attach must re-assert ENFORCED before attaching (belt-and-suspenders)")
	admin.mu.Unlock()

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(logBytes), "is-active devm.target",
		"RunShell must probe devm.target to recognize the warm-attach state")
	assert.NotContains(t, string(logBytes), "test -f /run/devm/provisioning",
		"an already-provisioned vm must short-circuit before the dirty-marker probe")
}

// TestRunShellWarmPath_ForwardsHostTermEnvIntoTartExec pins the color
// regression fix. The tart-migration refactor (c97bcc2) dropped the
// old sbx `-e KEY=VAL` env forwarding from attachShell; the guest
// bash then ran with an empty TERM, defaulted to dumb-mode, and TUIs
// showed no colors. Restored via terminalEnvForward() which chains
// through env(1) inside the argv. This test asserts the resulting
// `tart exec` argv actually contains that env prefix.
func TestRunShellWarmPath_ForwardsHostTermEnvIntoTartExec(t *testing.T) {
	t.Setenv("TERM", "xterm-ghostty")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("LANG", "en_US.UTF-8")
	// Explicitly unset the other two so the assertions don't depend on
	// whatever the test host happens to have exported.
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")

	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: true, Running: true},
	}
	tartBin, _ := fakeTartBinState(t, repoRoot, true, false)
	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	require.NotEmpty(t, spawner.started, "expected a spawn for tart exec")
	argv := spawner.started[0]
	// argv[0] is the tart binary path; the rest is [exec, ..., vmName, env, KEY=VAL..., wrapper, cmdName, ...]
	assert.Contains(t, argv, "env",
		"tart exec argv must include the env(1) prefix that carries host TERM/COLORTERM into the guest — this is the color fix")
	assert.Contains(t, argv, "TERM=xterm-ghostty",
		"host TERM must be forwarded so guest TUIs pick up the right terminfo")
	assert.Contains(t, argv, "COLORTERM=truecolor",
		"host COLORTERM must be forwarded so truecolor TUIs render correctly")
	assert.Contains(t, argv, "LANG=en_US.UTF-8",
		"host LANG must be forwarded for locale-sensitive TUIs (guest bootstrap generates en_US.UTF-8)")
	// Empty vars are dropped by terminalEnvForward — an empty LC_ALL
	// would poison the guest locale.
	for _, s := range argv {
		assert.NotEqual(t, "LC_ALL=", s, "empty LC_ALL must not be forwarded")
		assert.NotEqual(t, "LC_CTYPE=", s, "empty LC_CTYPE must not be forwarded")
	}
}

// TestRunShellColdPath_CallsStartAndProvisions verifies the cold-start
// sequence: StartVM is called, then waitVMReady polls exec, then the
// provisioner runs (here exercised as a no-op because tart is fake).
//
// We use a tart binary that succeeds immediately so waitVMReady returns
// right away; the provision step runs against a tart that exec's `true`
// successfully. The test only checks orchestration order, not provision
// output.
func TestRunShellColdPath_CallsStartVM(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp:     serviceapi.VMStatusResponse{Present: false, Running: false},
		startProjectIP: "127.42.0.5",
	}

	// Write a fake CA so ReadFile succeeds.
	caDir := filepath.Join(repoRoot, "ca")
	require.NoError(t, os.MkdirAll(caDir, 0o755))

	// Use a tart binary that always returns exit 0 so waitVMReady and
	// provision exec calls all succeed immediately.
	tartBin := fakeTartBin(t, repoRoot)

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}

	// Point caStorageDir at our temp dir by overriding HOME.
	t.Setenv("HOME", repoRoot)
	// Write the CA root in the place caStorageDir() will look.
	caPath := filepath.Join(repoRoot, "Library", "Application Support", "devm", "ca")
	require.NoError(t, os.MkdirAll(caPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(caPath, "root.crt"), []byte("FAKE-CA"), 0o644))

	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	admin.mu.Lock()
	assert.Equal(t, 1, admin.startCalled, "StartVM must be called exactly once on cold start")
	admin.mu.Unlock()

	// Regression: the daemon-side state snapshot must be seeded at the
	// end of a fully-green cold start, so the first `devm reconcile`
	// has a baseline instead of diffing against schema.Config{} (which
	// spuriously surfaces every teardown-bucket kind as pending).
	got, err := serviceapi.ReadStateSnapshot(minimalCfg().Project.Name)
	require.NoError(t, err)
	require.NotNil(t, got, "cold start must seed the daemon state snapshot")
	assert.Equal(t, minimalCfg().Project.Name, got.Cfg.Project.Name)
	// Regression: the seed snapshot must carry ProjectIP (from
	// VMStartResponse) so a daemon crash between /vm/start and the next
	// reconcile doesn't strand recoverProjectState with nothing to
	// restore.
	assert.Equal(t, "127.42.0.5", got.ProjectIP)
}

// TestRunShellColdPath_FlipsEgressAroundProvision verifies the Critical fix:
// the daemon-side softnet OPEN→ENFORCED flip happens BETWEEN the two
// provisioning execs, not around a single one — OpenEgress, then RunOpen's
// exec (apt/install:/templates/startup:), then ApplyEgressEnforcement, then
// RunEnforced's exec (services + devm.target). Without this order services
// would come up under open (unenforced) egress every boot.
//
// Both fakeVMAdmin (OpenEgress/ApplyEgressEnforcement) and the fake tart
// binary (the two provisioning ExecStreams' `bash -c`) append markers to the
// same log file, giving one ordered timeline across both fakes. The fake
// logs each invocation's full argv verbatim, and a multi-line script argv
// spans several physical lines in the log — so the two `bash -c` execs are
// told apart by ORDER (first occurrence = RunOpen, second = RunEnforced),
// not by grepping a single line for script content.
func TestRunShellColdPath_FlipsEgressAroundProvision(t *testing.T) {
	repoRoot := t.TempDir()
	logPath := filepath.Join(repoRoot, "order.log")
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: false, Running: false},
		logPath:    logPath,
	}

	tartBin := fakeTartBinWithLog(t, repoRoot, logPath)

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	writeFakeCA(t, repoRoot)

	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	admin.mu.Lock()
	assert.Equal(t, 1, admin.openEgressCalled, "OpenEgress must be called exactly once")
	assert.Equal(t, 1, admin.applyEgressEnforcementCalled, "ApplyEgressEnforcement must be called exactly once")
	assert.Equal(t, []string{"open-egress", "apply-egress-enforcement"}, admin.callOrder)
	admin.mu.Unlock()

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")

	openIdx, runOpenIdx, enforceIdx, runEnforcedIdx := -1, -1, -1, -1
	bashCSeen := 0
	for i, line := range lines {
		switch {
		case line == "OPEN-EGRESS":
			openIdx = i
		case line == "APPLY-EGRESS-ENFORCEMENT":
			enforceIdx = i
		case strings.Contains(line, "bash -c"):
			bashCSeen++
			switch bashCSeen {
			case 1:
				runOpenIdx = i
			case 2:
				runEnforcedIdx = i
			}
		}
	}
	require.GreaterOrEqual(t, openIdx, 0, "OPEN-EGRESS marker must be present")
	require.GreaterOrEqual(t, runOpenIdx, 0, "RunOpen's bash -c exec must be present")
	require.GreaterOrEqual(t, enforceIdx, 0, "APPLY-EGRESS-ENFORCEMENT marker must be present")
	require.GreaterOrEqual(t, runEnforcedIdx, 0, "RunEnforced's bash -c exec must be present")
	assert.Less(t, openIdx, runOpenIdx, "OpenEgress must run BEFORE RunOpen's exec")
	assert.Less(t, runOpenIdx, enforceIdx, "ApplyEgressEnforcement must run AFTER RunOpen's exec")
	assert.Less(t, enforceIdx, runEnforcedIdx,
		"RunEnforced's exec (services + devm.target) must run AFTER ApplyEgressEnforcement — "+
			"the Critical fix: services must never start under open egress")
}

// TestRunShellColdPath_PostInstallFail_KeepsVM verifies that a
// service-phase failure (the enforced script's `services` stage) leaves
// the VM running so the user can debug — install failures still tear down.
func TestRunShellColdPath_PostInstallFail_KeepsVM(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: false, Running: false},
	}
	// The single provisioning ExecStream emits the `services` stage marker
	// then exits non-zero — a broken user service. That stage is
	// post-install, so the VM must be kept.
	tartBin, logPath := fakeTartBinStageFail(t, repoRoot, "services")

	spawner := &stubSpawner{}
	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	t.Setenv("HOME", repoRoot)
	caPath := filepath.Join(repoRoot, "Library", "Application Support", "devm", "ca")
	require.NoError(t, os.MkdirAll(caPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(caPath, "root.crt"), []byte("FAKE-CA"), 0o644))

	_, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provision")

	admin.mu.Lock()
	assert.Equal(t, 0, admin.stopCalled, "StopVM must NOT be called on post-install failure")
	admin.mu.Unlock()

	if logBytes, err := os.ReadFile(logPath); err == nil {
		assert.NotContains(t, string(logBytes), "delete x-sbx",
			"tart delete must NOT run on post-install failure — VM is worth debugging in place")
	}
}

// TestRunShellColdPath_ProvisionFail_TearsDownVM verifies Bug B: when the
// provisioning script fails in an install-phase stage, RunShell asks the
// daemon to stop the VM AND invokes `tart delete` so no zombie VM is left.
func TestRunShellColdPath_ProvisionFail_TearsDownVM(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: false, Running: false},
	}

	// The RunOpen ExecStream emits the `install` stage marker then
	// exits non-zero — an install-phase failure that must tear down.
	tartBin, logPath := fakeTartBinStageFail(t, repoRoot, "install")

	spawner := &stubSpawner{}
	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	t.Setenv("HOME", repoRoot)
	caPath := filepath.Join(repoRoot, "Library", "Application Support", "devm", "ca")
	require.NoError(t, os.MkdirAll(caPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(caPath, "root.crt"), []byte("FAKE-CA"), 0o644))

	_, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provision")

	// Bug B assertions.
	admin.mu.Lock()
	assert.Equal(t, 1, admin.stopCalled, "StopVM must be called on provision failure")
	admin.mu.Unlock()

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(logBytes), "delete x-sbx",
		"tart delete <vm> must run on provision failure so the VM disk is destroyed")
}

// TestRunShellWarmPath_VMStatusError verifies that a daemon error on
// VMStatus surfaces as a RunShell error.
func TestRunShellWarmPath_VMStatusError(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusErr: fmt.Errorf("daemon down"),
	}

	deps := ShellDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		UserSpawner:      &stubSpawner{},
	}
	_, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query vm status")
}

// TestRunShellColdPath_StartVMError verifies that a daemon error on
// StartVM surfaces as a RunShell error.
func TestRunShellColdPath_StartVMError(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: false, Running: false},
		startErr:   fmt.Errorf("clone failed"),
	}

	deps := ShellDeps{
		Tart:             tartPathNotNeeded(t),
		ServiceAPIClient: admin,
		UserSpawner:      &stubSpawner{},
	}
	_, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start vm")
}

// TestRunShellRunning_TargetInactiveNoMarker_AdoptsInPlace verifies the
// adopt-in-place branch: the VM process is running, but devm.target isn't
// active and no dirty-provisioning marker is present — a pristine VM
// (started directly via `tart run`, or a clean daemon crash-restart before
// provisioning began). RunShell must provision the already-running VM
// WITHOUT asking the daemon to start it (no StartVM), then attach.
func TestRunShellRunning_TargetInactiveNoMarker_AdoptsInPlace(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: true, Running: true},
	}
	tartBin, logPath := fakeTartBinState(t, repoRoot, false, false)

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	writeFakeCA(t, repoRoot)

	// A non-trivial network allow-list (rather than minimalCfg's empty
	// one) so the ApplyIronProxy request's Allowlist assertion below
	// actually exercises docker.EffectiveAllowlist's plumbing instead
	// of trivially comparing nil to nil.
	cfg := minimalCfg()
	cfg.Network.Allow = []schema.AllowEntry{{Host: "example.com"}}
	cfg.Docker = true

	rc, err := RunShell(context.Background(), deps, cfg, repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	admin.mu.Lock()
	assert.Equal(t, 0, admin.startCalled, "adopt-in-place must NOT call StartVM — the vm is already running")
	assert.Equal(t, 0, admin.stopCalled, "adopt-in-place must not tear down a pristine vm")
	assert.Equal(t, 1, admin.applyIronProxyCalled,
		"adopt-in-place must revive this project's iron-proxy — StartVM (the only other "+
			"thing that spawns it) is skipped, and a prior `devm stop` tears iron-proxy "+
			"down with the vm")
	assert.Equal(t, docker.EffectiveAllowlist(cfg), admin.applyIronProxyReq.Allowlist,
		"adopt-in-place must pass the resolved effective allowlist (user hosts + docker hub) to ApplyIronProxy")
	assert.Empty(t, admin.applyIronProxyReq.Secrets,
		"minimalCfg declares no !secret refs, so resolved bindings must be empty")
	admin.mu.Unlock()

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(logBytes), "is-active devm.target",
		"RunShell must probe devm.target to distinguish warm-attach from adopt")
	assert.Contains(t, string(logBytes), "test -f /run/devm/provisioning",
		"RunShell must probe the dirty-provisioning marker when devm.target is inactive")
	assert.NotContains(t, string(logBytes), "delete x-sbx",
		"adopt-in-place must not delete the vm it's adopting")

	require.NotEmpty(t, spawner.started, "expected the adopted vm to be attached to")
}

// TestRunShellRunning_AdoptInPlace_NoIronProxyRecord_FailsLoud verifies
// that adopt-in-place surfaces a clear error (rather than falling through
// to a confusing EnforcementConfig 412) when ApplyIronProxy reports no
// iron-proxy record exists at all for this project — a VM that was never
// started by devm in the first place.
func TestRunShellRunning_AdoptInPlace_NoIronProxyRecord_FailsLoud(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp:            serviceapi.VMStatusResponse{Present: true, Running: true},
		applyIronProxyResp:    serviceapi.VMApplyIronProxyResponse{Applied: false, VMRunning: false},
		applyIronProxyRespSet: true,
	}
	tartBin, _ := fakeTartBinState(t, repoRoot, false, false)
	spawner := &stubSpawner{}

	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	writeFakeCA(t, repoRoot)

	_, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no iron-proxy record found")

	admin.mu.Lock()
	assert.Equal(t, 1, admin.applyIronProxyCalled)
	assert.Equal(t, 0, admin.stopCalled, "must not tear down the vm just because it lacks an iron-proxy record")
	admin.mu.Unlock()
}

// TestRunShellRunning_TargetInactiveMarkerPresent_TeardownAndColdStart
// verifies the dirty-teardown branch: the VM process is running, devm.target
// isn't active, and the dirty-provisioning marker IS present — a previous
// provisioning run was interrupted, leaving the guest in an unknown
// intermediate state. RunShell must never provision onto that slate: it
// tears the VM down (stop + delete) and falls through to a fresh cold
// start (StartVM + waitVMReady + provision + attach).
func TestRunShellRunning_TargetInactiveMarkerPresent_TeardownAndColdStart(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: true, Running: true},
	}
	tartBin, logPath := fakeTartBinState(t, repoRoot, false, true)

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	writeFakeCA(t, repoRoot)

	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	admin.mu.Lock()
	assert.Equal(t, 1, admin.stopCalled, "a dirty (interrupted-provisioning) vm must be stopped before a fresh cold start")
	assert.Equal(t, 1, admin.startCalled, "after tearing down a dirty vm, RunShell must cold-start a fresh one")
	admin.mu.Unlock()

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(logBytes), "delete x-sbx",
		"a dirty vm must be deleted before the fresh cold start — never provision onto a dirty slate")

	require.NotEmpty(t, spawner.started, "expected the freshly cold-started vm to be attached to")
}

// TestRunShellRunning_TargetProbeTransportFlake_RetriesThenWarmAttaches
// verifies the devm.target probe uses ExecWithRetry, not Exec: a single
// tart-guest-agent transport flake must not misclassify a warm,
// provisioned vm as "not provisioned" — which would fall through to the
// dirty/adopt checks and risk a needless re-provision of a healthy vm.
func TestRunShellRunning_TargetProbeTransportFlake_RetriesThenWarmAttaches(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: true, Running: true},
	}
	tartBin, logPath := fakeTartBinTargetProbeFlake(t, repoRoot)

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	admin.mu.Lock()
	assert.Equal(t, 0, admin.startCalled, "a single transport flake on the target probe must not trigger a re-provision path")
	assert.Equal(t, 0, admin.stopCalled, "a single transport flake on the target probe must not trigger teardown")
	admin.mu.Unlock()

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(logBytes), "is-active devm.target"),
		"ExecWithRetry must retry the flaked probe exactly once")
	assert.NotContains(t, string(logBytes), "test -f /run/devm/provisioning",
		"the retried probe must resolve to provisioned — the dirty-marker probe must not run")
}

// TestRunShellRunning_DirtyProbeTransportFlake_RetriesThenAdoptsInPlace
// verifies the dirty-provisioning-marker probe uses ExecWithRetry, not
// Exec: a single transport flake must not misclassify a pristine vm as
// dirty — which would tear it down needlessly instead of adopting it in
// place.
func TestRunShellRunning_DirtyProbeTransportFlake_RetriesThenAdoptsInPlace(t *testing.T) {
	repoRoot := t.TempDir()
	admin := &fakeVMAdmin{
		statusResp: serviceapi.VMStatusResponse{Present: true, Running: true},
	}
	tartBin, logPath := fakeTartBinDirtyProbeFlake(t, repoRoot)

	userCmd := &stubCmd{waitErr: make(chan error, 1)}
	userCmd.waitErr <- nil
	spawner := &stubSpawner{cmdQueue: []*stubCmd{userCmd}}

	deps := ShellDeps{
		Tart:             tartBin,
		ServiceAPIClient: admin,
		UserSpawner:      spawner,
	}
	writeFakeCA(t, repoRoot)

	rc, err := RunShell(context.Background(), deps, minimalCfg(), repoRoot, "x-sbx", "bash", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, rc)

	admin.mu.Lock()
	assert.Equal(t, 0, admin.startCalled, "adopt-in-place must not call StartVM")
	assert.Equal(t, 0, admin.stopCalled, "a single transport flake on the dirty probe must not trigger teardown")
	assert.Equal(t, 1, admin.applyIronProxyCalled,
		"the retried probe must resolve to pristine, taking the adopt-in-place path")
	admin.mu.Unlock()

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(logBytes), "test -f /run/devm/provisioning"),
		"ExecWithRetry must retry the flaked probe exactly once")
	assert.NotContains(t, string(logBytes), "delete x-sbx",
		"a pristine vm must not be torn down")
}

// fakeTartBinTargetProbeFlake returns a fake tart binary that flakes
// exactly once on the "is-active devm.target" probe — emitting a
// tart-guest-agent transport-inactive marker on stderr and exiting 1 —
// and reports the vm provisioned (exit 0) on the retry. Every other
// probe reports absent, so a successfully-retried target probe lands on
// the warm-attach path.
func fakeTartBinTargetProbeFlake(t *testing.T, dir string) (*tart.Tart, string) {
	t.Helper()
	bin := filepath.Join(dir, "tart-fake-target-flake")
	logPath := filepath.Join(dir, "tart-invocations.log")
	counterPath := filepath.Join(dir, "target-flake-counter")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$*" in
  *"is-active devm.target"*)
    c=$(cat %q 2>/dev/null || echo 0)
    c=$((c+1))
    echo "$c" > %q
    if [ "$c" -eq 1 ]; then
      echo "Transport became inactive" >&2
      exit 1
    fi
    exit 0
    ;;
  *"test -f /run/devm/provisioning"*) exit 1 ;;
  *"test -f /var/lib/devm/provisioned"*) exit 1 ;;
esac
exit 0
`, logPath, counterPath, counterPath)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr, logPath
}

// fakeTartBinDirtyProbeFlake returns a fake tart binary where
// "is-active devm.target" always reports inactive (a real, non-flaky
// result) and "test -f /run/devm/provisioning" flakes exactly once — a
// transport-inactive marker on stderr, exit 1 — before resolving to
// "absent" (exit 1, i.e. pristine, not dirty) on the retry.
func fakeTartBinDirtyProbeFlake(t *testing.T, dir string) (*tart.Tart, string) {
	t.Helper()
	bin := filepath.Join(dir, "tart-fake-dirty-flake")
	logPath := filepath.Join(dir, "tart-invocations.log")
	counterPath := filepath.Join(dir, "dirty-flake-counter")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$*" in
  *"is-active devm.target"*) exit 1 ;;
  *"test -f /run/devm/provisioning"*)
    c=$(cat %q 2>/dev/null || echo 0)
    c=$((c+1))
    echo "$c" > %q
    if [ "$c" -eq 1 ]; then
      echo "Transport became inactive" >&2
      exit 1
    fi
    exit 1
    ;;
  *"test -f /var/lib/devm/provisioned"*) exit 1 ;;
esac
exit 0
`, logPath, counterPath, counterPath)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr, logPath
}

// tartPathNotNeeded returns a *tart.Tart whose binary is "false" —
// it will exit 1 immediately if called. Use this when the test is
// exercising a path that should never invoke tart.
func tartPathNotNeeded(t *testing.T) *tart.Tart {
	t.Helper()
	tr := tart.New()
	tr.Path = "false"
	return tr
}

// fakeTartBin writes a shell script into dir that exits 0 for all
// subcommands, and returns a *tart.Tart pointing at it.
func fakeTartBin(t *testing.T, dir string) *tart.Tart {
	t.Helper()
	bin := filepath.Join(dir, "tart-fake")
	script := "#!/bin/sh\nexec true\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr
}

// fakeTartBinWithLog writes a fake tart binary that succeeds for every
// invocation and appends each one to the given (caller-supplied) logPath —
// used to interleave with a fakeVMAdmin's own log writes into one ordered
// timeline. The first-boot marker probe reports absent so cold-start takes
// the first-boot path.
func fakeTartBinWithLog(t *testing.T, dir, logPath string) *tart.Tart {
	t.Helper()
	bin := filepath.Join(dir, "tart-fake-log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$*" in
  *"test -f /var/lib/devm/provisioned"*) exit 1 ;;
esac
exit 0
`, logPath)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr
}

// fakeTartBinStageFail writes a fake tart binary that logs every
// invocation and, for the ONE of the two provisioning ExecStreams (`bash -c
// <script>`) that stage actually runs in, emits the given
// `::devm:stage:<stage>::` marker on stdout and exits non-zero —
// simulating a script failure at that stage. "enforce" and "services" run
// in RunEnforced's exec (identified by the enforce-stage marker baked into
// its script); every other stage
// (open/packages/install/docker/templates/startup) runs in RunOpen's exec
// (identified by the bundle-tar extraction baked into its script) — the
// OTHER exec succeeds normally, exactly as the real split scripts behave.
// The first-boot marker probe (`test -f /var/lib/devm/provisioned`) reports
// absent so cold-start takes the first-boot path. Every other call
// (waitVMReady `true`, teardown `delete`) succeeds / is logged.
func fakeTartBinStageFail(t *testing.T, dir, stage string) (*tart.Tart, string) {
	t.Helper()
	bin := filepath.Join(dir, "tart-fake-stagefail")
	logPath := filepath.Join(dir, "tart-invocations.log")
	failMarker := `*"tar -xC /opt/devm"*` // RunOpen's exec
	if stage == "enforce" || stage == "services" {
		failMarker = `*"::devm:stage:enforce::"*` // RunEnforced's exec
	}
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$*" in
  *"test -f /var/lib/devm/provisioned"*) exit 1 ;;
  %s) echo "::devm:stage:%s::"; exit 1 ;;
  *"bash -c"*) exit 0 ;;
esac
exit 0
`, logPath, failMarker, stage)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr, logPath
}

// fakeTartBinState returns a fake tart binary for exercising RunShell's
// four-way running-VM branch. targetActive and markerPresent set the exit
// codes of the `systemctl is-active devm.target` / `test -f
// /run/devm/provisioning` probes; the first-boot marker probe (`test -f
// /var/lib/devm/provisioned`) always reports absent so any composed-script
// provisioning that runs takes the first-boot path. Every other invocation
// (waitVMReady's `true`, the provisioning ExecStream, `tart delete`, `tart
// list`) succeeds. Every invocation is appended to logPath for assertions.
func fakeTartBinState(t *testing.T, dir string, targetActive, markerPresent bool) (*tart.Tart, string) {
	t.Helper()
	bin := filepath.Join(dir, "tart-fake-state")
	logPath := filepath.Join(dir, "tart-invocations.log")
	targetExit, markerExit := 1, 1
	if targetActive {
		targetExit = 0
	}
	if markerPresent {
		markerExit = 0
	}
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$*" in
  *"is-active devm.target"*) exit %d ;;
  *"test -f /run/devm/provisioning"*) exit %d ;;
  *"test -f /var/lib/devm/provisioned"*) exit 1 ;;
esac
exit 0
`, logPath, targetExit, markerExit)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	tr := tart.New()
	tr.Path = bin
	return tr, logPath
}

// writeFakeCA points caStorageDir() at repoRoot (via HOME) and seeds a
// fake CA root, satisfying provisionAndAttach's CA read.
func writeFakeCA(t *testing.T, repoRoot string) {
	t.Helper()
	t.Setenv("HOME", repoRoot)
	caPath := filepath.Join(repoRoot, "Library", "Application Support", "devm", "ca")
	require.NoError(t, os.MkdirAll(caPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(caPath, "root.crt"), []byte("FAKE-CA"), 0o644))
}

// ---------- resolveSecretBindings tests ----------

func secretRef(name string) schema.EnvValue {
	return schema.EnvValue{Secret: &schema.SecretRef{Name: name}}
}

func TestResolveSecretBindings(t *testing.T) {
	t.Run("secret_with_host_scope", func(t *testing.T) {
		// A secret named under a host allow-entry comes back with Hosts populated.
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/gh", "token123"))

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Env:     map[string]schema.EnvValue{"TOKEN": secretRef("gh")},
			Network: schema.Network{
				Allow: []schema.AllowEntry{
					{Host: "api.github.com", Secrets: []string{"gh"}},
				},
			},
		}

		bindings, err := resolveSecretBindings(cfg, be)
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.Equal(t, "gh", bindings[0].Name)
		assert.Equal(t, "token123", bindings[0].Value)
		assert.Equal(t, []string{"api.github.com"}, bindings[0].Hosts)
	})

	t.Run("secret_with_no_host_scope", func(t *testing.T) {
		// A secret referenced in env but bound to NO allow-entry host comes
		// back with empty/nil Hosts (iron-proxy never injects it).
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/mytoken", "secret_value"))

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Env:     map[string]schema.EnvValue{"MY_TOKEN": secretRef("mytoken")},
			Network: schema.Network{
				Allow: []schema.AllowEntry{
					{Host: "example.com"}, // no secrets listed
				},
			},
		}

		bindings, err := resolveSecretBindings(cfg, be)
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.Equal(t, "mytoken", bindings[0].Name)
		assert.Equal(t, "secret_value", bindings[0].Value)
		assert.Empty(t, bindings[0].Hosts)
	})

	t.Run("missing_keychain_entry_returns_error", func(t *testing.T) {
		// A !secret whose keychain entry is missing → error mentioning the name.
		be := secret.NewFake()
		// Deliberately do NOT seed "proj/missing".

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Env:     map[string]schema.EnvValue{"TOKEN": secretRef("missing")},
		}

		_, err := resolveSecretBindings(cfg, be)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing")
	})

	t.Run("secret_under_two_hosts_gets_both_sorted", func(t *testing.T) {
		// A secret named under two allow entries comes back with both hosts sorted.
		be := secret.NewFake()
		require.NoError(t, be.Set("proj/tok", "val"))

		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Env:     map[string]schema.EnvValue{"T": secretRef("tok")},
			Network: schema.Network{
				Allow: []schema.AllowEntry{
					{Host: "z.example.com", Secrets: []string{"tok"}},
					{Host: "a.example.com", Secrets: []string{"tok"}},
				},
			},
		}

		bindings, err := resolveSecretBindings(cfg, be)
		require.NoError(t, err)
		require.Len(t, bindings, 1)
		assert.Equal(t, []string{"a.example.com", "z.example.com"}, bindings[0].Hosts)
	})

	t.Run("no_secrets_returns_nil", func(t *testing.T) {
		be := secret.NewFake()
		cfg := schema.Config{
			Project: schema.Project{Name: "proj"},
			Env:     map[string]schema.EnvValue{"PLAIN": {Literal: "value"}},
		}
		bindings, err := resolveSecretBindings(cfg, be)
		require.NoError(t, err)
		assert.Nil(t, bindings)
	})
}
