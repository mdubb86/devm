package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/hostinfo"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/ironproxy"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/provision"
	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/secret"
	"github.com/mdubb86/devm/internal/serviceapi"
	"github.com/mdubb86/devm/internal/serviceapi/sshkeys"
	"github.com/mdubb86/devm/internal/status"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// ShellDeps wires the orchestrator's collaborators. Production callers
// build one via DefaultShellDeps; tests substitute fakes.
type ShellDeps struct {
	Tart             *tart.Tart
	ServiceAPIClient VMAdminClient
	// UserSpawner runs the interactive shell command. Production code
	// uses ExecSpawner; tests use a stub.
	UserSpawner Spawner
	// Ident is the daemon identity (prod vs. e2e) this shell session
	// operates under — threaded into every serviceapi/sshkeys call
	// RunShell's methods make, instead of a hardcoded identity.Prod.
	Ident identity.Config
}

// VMAdminClient is the subset of serviceapi.Client used by RunShell.
// Extracted as an interface so tests can inject a fake.
type VMAdminClient interface {
	VMStatus(ctx context.Context, name string) (serviceapi.VMStatusResponse, error)
	StartVM(ctx context.Context, req serviceapi.VMStartRequest) (serviceapi.VMStartResponse, error)
	EnforcementConfig(ctx context.Context, name string) (serviceapi.VMEnforcementConfigResponse, error)
	StopVM(ctx context.Context, name string, destroy bool) error
	// ApplyIronProxy (re)spawns this project's iron-proxy on its
	// existing MAC_HOST/ports without touching the VM — the same
	// no-VM-cycle primitive `devm reconcile`'s self-heal
	// (BucketEgressRestart) uses. Adopt-in-place needs it: a prior
	// `devm stop` tears iron-proxy down along with the VM, so a VM
	// adopted after a raw `tart run` may have no live iron-proxy even
	// though the VM process itself is up.
	ApplyIronProxy(ctx context.Context, req serviceapi.VMApplyIronProxyRequest) (serviceapi.VMApplyIronProxyResponse, error)
	// BeginProvisioning flips the project's softnet control socket to
	// ENFORCED-behavior (:80/:443 route to iron-proxy) and the egress
	// policy authority to passthrough. Called post-RunBundle — the guest
	// trust store already has the iron-proxy CA at this point — and
	// pre-volume-sync. iron-proxy is in the traffic path for the rest of
	// the VM's life from this call onward.
	BeginProvisioning(ctx context.Context, name string) error
	// VolumeSync establishes a mutagen sync session for every entity in
	// cfg (volumes and repos alike). Called after BeginProvisioning and
	// before RepoClone.
	VolumeSync(ctx context.Context, name string, cfg schema.Config, repoRoot string) error
	// RepoClone runs a cold-start git clone, through iron-proxy, for
	// every repo entity in cfg where the relevant sides are empty.
	// Called after VolumeSync — the mutagen sessions it establishes pick
	// up the freshly-cloned guest content on their own.
	RepoClone(ctx context.Context, name string, cfg schema.Config, repoRoot string, tunnelPort int) error
	// EndProvisioning flips the egress policy authority back to
	// restricted. Called pre-RunEnforced — the Critical fix: services
	// must never start except under the project's real allowlist.
	// Softnet stays in ENFORCED-behavior; only the authority mode
	// changes here. warmAttach also calls this, idempotently, as a
	// defense-in-depth re-assertion before attaching to an already-warm
	// VM.
	EndProvisioning(ctx context.Context, name string) error
}

// DefaultShellDeps returns deps wired for production.
func DefaultShellDeps(cfg identity.Config, repoRoot string) ShellDeps {
	return ShellDeps{
		Tart:             tart.New(),
		ServiceAPIClient: serviceapi.NewClient(cfg),
		UserSpawner:      &ExecSpawner{Interactive: true},
		Ident:            cfg,
	}
}

// RunShell implements `devm shell`. Returns the user shell's exit code
// and a non-nil error only when an orchestration step itself failed.
func RunShell(ctx context.Context, d ShellDeps, cfg schema.Config, repoRoot, vmName, cmdName string, cmdArgs []string) (int, error) {
	reporter := status.New(os.Stderr)
	defer reporter.Stop()
	reporter.Start("starting up")

	// Check VM state via daemon admin.
	vmStatus, err := d.ServiceAPIClient.VMStatus(ctx, cfg.Project.Name)
	if err != nil {
		return -1, fmt.Errorf("query vm status: %w", err)
	}
	log.Printf("shell: vm status: present=%v running=%v", vmStatus.Present, vmStatus.Running)

	if vmStatus.Running {
		// The VM process is up, but that alone doesn't tell us whether it's
		// provisioned. Probe devm.target (gates access until provisioning's
		// service-start phase succeeds) to find out which of three states
		// we're in.
		//
		// ExecWithRetry, not Exec: this probe drives the security-sensitive
		// warm/adopt/dirty branch below. A transient guest-agent transport
		// flake here (ExitCode -1) would misread a warm, provisioned VM as
		// "not provisioned", falling into the dirty/adopt checks and risking
		// a needless re-provision. A genuine "not active" is a clean
		// non-zero exit (not a transport flake), so it is not retried.
		provisioned := d.Tart.ExecWithRetry(ctx, vmName,
			[]string{"systemctl", "is-active", "devm.target"}).ExitCode == 0
		if provisioned {
			return d.warmAttach(ctx, vmName, repoRoot, cmdName, cmdArgs, reporter)
		}

		// Not provisioned. /run/devm/provisioning is written before the
		// composed script starts and removed when it finishes (render's
		// inProgressMarker) — its presence means a previous provisioning
		// run was interrupted (daemon crash, host sleep, killed exec) and
		// left the guest in an unknown intermediate state.
		//
		// ExecWithRetry, not Exec: a transport flake here (ExitCode -1)
		// would misread a dirty VM as clean, adopting-in-place onto an
		// unknown intermediate state instead of tearing it down.
		dirty := d.Tart.ExecWithRetry(ctx, vmName,
			[]string{"test", "-f", "/run/devm/provisioning"}).ExitCode == 0
		if dirty {
			// Never provision onto a dirty slate: tear down and fall
			// through to a fresh cold start below.
			reporter.Step("recovering (teardown + fresh start)", false)
			if err := d.teardownVM(ctx, cfg, vmName); err != nil {
				return -1, fmt.Errorf("teardown dirty vm: %w", err)
			}
		} else {
			// Pristine: running but never provisioned (direct `tart run`,
			// or a clean daemon crash-restart before provisioning began).
			// Adopt in place — provision it without StartVM/waitVMReady,
			// since it's already up and exec-ready.
			reporter.Step("adopting running vm", false)
			bindings, err := serviceapi.ResolveSecretBindings(cfg, secret.NewFileBackend(d.Ident.SecretsDir()), repoRoot)
			if err != nil {
				return -1, fmt.Errorf("resolve secrets: %w", err)
			}
			repoHosts, err := serviceapi.RepoHosts(cfg, repoRoot)
			if err != nil {
				return -1, fmt.Errorf("resolve repo hosts: %w", err)
			}
			// Adopt-in-place deliberately skips StartVM below (the VM
			// process is already up), but StartVM is also the only
			// thing that normally (re)spawns this project's iron-proxy.
			// Revive it explicitly on its last-known MAC_HOST/ports so
			// the provisioning tail's EnforcementConfig fetch (next,
			// inside provisionAndAttach) has a live iron-proxy to read.
			applyResp, err := d.ServiceAPIClient.ApplyIronProxy(ctx, serviceapi.VMApplyIronProxyRequest{
				Name:      cfg.Project.Name,
				Allowlist: serviceapi.AppendUniqueHosts(docker.EffectiveAllowlist(cfg), repoHosts),
				Secrets:   bindings,
			})
			if err != nil {
				return -1, fmt.Errorf("ensure iron-proxy for adopt-in-place: %w", err)
			}
			if !applyResp.Applied && !applyResp.VMRunning {
				return -1, fmt.Errorf(
					"adopt-in-place: no iron-proxy record found for %q — this vm was never started by devm",
					cfg.Project.Name)
			}
			return d.provisionAndAttach(ctx, cfg, vmName, repoRoot, cmdName, cmdArgs, bindings, applyResp.ProjectIP, applyResp.TunnelPort, reporter)
		}
	}

	// Cold start: the VM was stopped, or we just tore down a dirty one above.
	reporter.Step("starting vm", false)
	log.Printf("shell: cold-start: sending StartVM to daemon")

	bindings, err := serviceapi.ResolveSecretBindings(cfg, secret.NewFileBackend(d.Ident.SecretsDir()), repoRoot)
	if err != nil {
		return -1, fmt.Errorf("resolve secrets: %w", err)
	}
	repoHosts, err := serviceapi.RepoHosts(cfg, repoRoot)
	if err != nil {
		return -1, fmt.Errorf("resolve repo hosts: %w", err)
	}

	// Collect allow-list from network config, expanded with Docker Hub
	// hosts when docker: true, plus any hosts implied by repo:
	// declarations so a bare `repo.secret:`/`repo.url:` doesn't also
	// require a matching network.allow entry for the clone to reach its
	// host (gap #1 of the repo-workspace hydration fixes).
	allowList := serviceapi.AppendUniqueHosts(docker.EffectiveAllowlist(cfg), repoHosts)

	var diskGB int
	if cfg.Disk != nil {
		diskGB, err = schema.ParseDiskSize(*cfg.Disk)
		if err != nil {
			return -1, fmt.Errorf("parse disk: %w", err)
		}
	}
	var memoryMB int
	if cfg.Memory != nil {
		memoryMB, err = schema.ParseMemorySize(*cfg.Memory)
		if err != nil {
			return -1, fmt.Errorf("parse memory: %w", err)
		}
	}
	var cpuCount int
	if cfg.Cpu != nil {
		cpuCount = *cfg.Cpu
	}
	startResp, err := d.ServiceAPIClient.StartVM(ctx, serviceapi.VMStartRequest{
		Name:       cfg.Project.Name,
		MacCwd:     repoRoot,
		AllowList:  allowList,
		Secrets:    bindings,
		DiskSizeGB: diskGB,
		MemoryMB:   memoryMB,
		CpuCount:   cpuCount,
		Cfg:        cfg,
	})
	if err != nil {
		return -1, fmt.Errorf("start vm: %w", err)
	}

	// Wait for VM to accept exec connections.
	reporter.Step("waiting for vm ready", false)
	if err := waitVMReady(ctx, d.Tart, vmName, 60*time.Second); err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "vm did not become ready")
	}
	log.Printf("shell: cold-start: vm exec-ready")

	return d.provisionAndAttach(ctx, cfg, vmName, repoRoot, cmdName, cmdArgs, bindings, startResp.ProjectIP, startResp.TunnelPort, reporter)
}

// warmAttach attaches to a VM that's already provisioned (devm.target
// active) — no reconciliation, no provisioning, just attach.
func (d ShellDeps) warmAttach(ctx context.Context, vmName, repoRoot, cmdName string, cmdArgs []string, reporter status.Reporter) (int, error) {
	// Warm attach: reconcile is handled by the provisioner on cold start.
	// For now the warm path just attaches directly.
	reporter.Step("attaching to running vm", false)

	// Defense-in-depth: re-assert the restricted egress authority mode
	// before attaching, even though this VM should already be restricted
	// from its own cold start. Idempotent.
	if err := d.ServiceAPIClient.EndProvisioning(ctx, vmName); err != nil {
		return -1, fmt.Errorf("end provisioning: %w", err)
	}

	reporter.Step("ready", false)
	reporter.Stop()
	reporter.Clear()
	if err := EmitSSHConfig(ctx, d.Ident, d.Tart); err != nil {
		log.Printf("ssh_config emit failed on warm attach: %v", err)
	}
	return d.attachShell(ctx, vmName, repoRoot, cmdName, cmdArgs)
}

// provisionAndAttach runs the provisioning + attach tail shared by
// cold-start (called after StartVM/waitVMReady) and adopt-in-place (called
// directly — the VM is already running and exec-ready). Any failure here
// tears the VM down unless it's a post-install failure, in which case the
// VM is kept running for in-place debugging (test_51's contract).
//
// Sequence: prov.RunBundle (extracts /opt/devm + installs the CA
// iron-proxy needs to MITM a guest git clone, offline, still under
// LOCKED), BeginProvisioning (softnet ENFORCED-behavior + authority
// passthrough), volumeSyncFn (mutagen sessions for every entity),
// repoCloneFn (cold-start guest git clone through the now CA-trusted
// iron-proxy) + waitForInitialSyncFn, prov.RunUser
// (packages/install:/docker/templates/startup:), runStartupCommandsFn,
// EndProvisioning (authority restricted), prov.RunEnforced (services +
// devm.target) — the last two run in that order so services never come
// up except under the project's real allowlist.
//
// projectIP is the project's daemon-allocated 127.42/16 loopback IP —
// from VMStartResponse on the cold-start path, VMApplyIronProxyResponse
// on adopt-in-place — seeded into the cold-start StateSnapshot below so
// a daemon crash before the next reconcile doesn't strand
// recoverProjectState with nothing to restore.
//
// tunnelPort is iron-proxy's CONNECT-capable tunnel_listen port, from
// the same two responses — the repo-clone stage below needs it to
// build the guest-visible HTTP_PROXY URL.
func (d ShellDeps) provisionAndAttach(ctx context.Context, cfg schema.Config, vmName, repoRoot, cmdName string, cmdArgs []string, bindings []serviceapi.SecretBinding, projectIP string, tunnelPort int, reporter status.Reporter) (int, error) {
	caPEM, err := os.ReadFile(filepath.Join(caStorageDir(d.Ident), "root.crt"))
	if err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "read CA root")
	}
	authPub, err := sshkeys.EnsureProjectKeypair(d.Ident, cfg.Project.Name)
	if err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "ensure project ssh keypair")
	}
	hostPriv, hostPub, err := sshkeys.EnsureProjectHostKey(d.Ident, cfg.Project.Name)
	if err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "ensure project ssh host key")
	}

	// timesyncd's NTP config is baked into the base image now (image/
	// provision-base.sh), not fetched/applied here. EnforcementConfig is
	// still called as a precondition check — this project's iron-proxy
	// state must exist before provisioning proceeds — mirroring the same
	// check EndProvisioning/BeginProvisioning make just below.
	if _, err := d.ServiceAPIClient.EnforcementConfig(ctx, cfg.Project.Name); err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "fetch enforcement config")
	}

	// Package drift for an EXISTING VM (non-first boot): diff the
	// last-applied snapshot's packages against the current config so the
	// boot script converges apt in its open window. Snapshot missing
	// (fresh VM) → first boot installs the full list anyway, and the
	// renderer ignores these fields on FirstBoot regardless.
	var pkgAdds, pkgRemoves []string
	if snap, err := serviceapi.ReadStateSnapshot(d.Ident, cfg.Project.Name); err == nil && snap != nil {
		pkgAdds, pkgRemoves = reconcile.PackageDrift(snap.Cfg, cfg)
	}

	// Best-effort: resolve the Mac's IANA zone so the guest can mirror
	// it in the bundle stage. On failure, log and leave the guest at
	// its baked UTC default — provisioning must not depend on this.
	macTz, tzErr := hostinfo.ResolveMacTimezone()
	if tzErr != nil {
		log.Printf("orchestrator: resolve mac timezone: %v — guest stays at UTC", tzErr)
	}
	prov := &provision.Provisioner{
		Tart:                d.Tart,
		VMName:              vmName,
		Cfg:                 cfg,
		CARootPEM:           caPEM,
		SSHAuthorizedPubkey: authPub,
		SSHHostPriv:         hostPriv,
		SSHHostPub:          hostPub,
		WorkspaceVMPath:     repoRoot,
		MacCwd:              repoRoot,
		DaemonRuntimeDir:    d.Ident.RuntimeDir(),
		StepTimeoutSeconds:  installStepTimeoutSeconds(),
		PackageAdds:         pkgAdds,
		PackageRemoves:      pkgRemoves,
		MacTimezone:         macTz,
	}
	log.Printf("shell: provisioning %s", vmName)
	reporter.Step("provisioning", false)

	// Provisioning output is DIAGNOSTIC — stage names, package install
	// noise, etc. It belongs on stderr so `devm exec pwd` / `devm shell
	// -- <cmd>` produce clean stdout that scripts can pipe. Failure details
	// flow via the returned error plus pp.FailureOutput() below, not via
	// this writer. pp drives the stage-marker spinner from ExecStream's
	// line-by-line output.
	pp := newProvisionProgress(reporter)

	// RunBundle extracts /opt/devm and runs install.sh — CA install,
	// mutagen-agent, systemd units, ssh material. Runs first, offline,
	// still under LOCKED: the guest's trust store must have the CA
	// iron-proxy needs to MITM a guest git clone before iron-proxy comes
	// into the traffic path at all.
	if err := prov.RunBundle(ctx, os.Stderr, pp.Line); err != nil {
		fmt.Fprint(os.Stderr, pp.FailureOutput())
		return d.teardownOnFail(ctx, cfg, vmName, err, "provision bundle")
	}
	log.Printf("shell: bundle extracted: %s", vmName)

	// softnet boots LOCKED. Flip it to ENFORCED-behavior and the egress
	// authority to passthrough now that the guest trusts the CA —
	// iron-proxy is in the traffic path for the rest of the VM's life
	// from this call onward.
	if err := d.ServiceAPIClient.BeginProvisioning(ctx, vmName); err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "begin provisioning")
	}

	// volume-sync: uniform mutagen session setup for every entity.
	fmt.Fprintln(os.Stderr, "::devm:stage:volume-sync::")
	if err := volumeSyncFn(d, ctx, cfg, vmName, repoRoot); err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "volume sync")
	}
	log.Printf("shell: volume sync ready: %s", vmName)

	// repo-clone: cold-start git clone, through the now CA-trusted
	// iron-proxy, for every repo entity where the relevant sides are
	// empty. Runs after volume-sync so the mutagen sessions already
	// established pick up the freshly-cloned guest content on their own.
	fmt.Fprintln(os.Stderr, "::devm:stage:repo-clone::")
	if err := repoCloneFn(d, ctx, cfg, vmName, repoRoot, tunnelPort); err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "repo clone")
	}
	log.Printf("shell: repo clone done: %s", vmName)

	// Wait for every session's initial sync to settle before install:/
	// startup: read the workspace — otherwise those steps could race a
	// still-converging first sync.
	if err := waitForInitialSyncFn(d, ctx, cfg, vmName); err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "wait for initial sync")
	}

	if err := prov.RunUser(ctx, os.Stderr, pp.Line); err != nil {
		fmt.Fprint(os.Stderr, pp.FailureOutput())
		// User-phase failures (apt/install:/docker/templates/startup:) tear
		// down — the VM is in a cold-start-broken state and the user's fix
		// belongs in devm.yaml (test_51: install failure = state=absent).
		return d.teardownOnFail(ctx, cfg, vmName, err, "provision")
	}
	log.Printf("shell: provisioning (passthrough egress) done: %s", vmName)

	// Fire per-repo startup commands under passthrough, right after
	// RunUser — the workspace volume-sync/repo-clone just hydrated is
	// exactly what install:/startup: need to see. Running before
	// EndProvisioning means a startup command isn't limited to the
	// project's network.allow: list for this window.
	if err := runStartupCommandsFn(d, ctx, cfg, vmName, repoRoot); err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "run startup commands")
	}
	log.Printf("shell: startup commands done: %s", vmName)

	// Flip the egress policy authority back to restricted BEFORE services
	// or devm.target come up — the Critical fix: services must never
	// start except under the project's real allowlist. Softnet stays in
	// ENFORCED-behavior; iron-proxy remains in the traffic path.
	if err := d.ServiceAPIClient.EndProvisioning(ctx, vmName); err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "end provisioning")
	}

	if err := prov.RunEnforced(ctx, os.Stderr, pp.Line); err != nil {
		fmt.Fprint(os.Stderr, pp.FailureOutput())
		// Service-phase failures (unit install, daemon-reload, enable+start)
		// leave the VM in a debuggable state — user's fix is
		// in devm.yaml, not in the VM. Surface the error but keep the VM
		// alive so `tart exec <vm> systemctl status` etc. works. Enforce-
		// phase failures (the daemon's own enforcement broken) still tear
		// down.
		if provision.IsPostInstallFailure(err) {
			log.Printf("shell: post-install failure — keeping VM: %v", err)
			return -1, fmt.Errorf("provision: %w", err)
		}
		return d.teardownOnFail(ctx, cfg, vmName, err, "provision")
	}
	log.Printf("shell: provisioning done: %s", vmName)

	// Write initial guest snapshot so subsequent `devm reconcile` calls
	// have a baseline to diff against. Without this, ReadSnapshot returns
	// "" which reconcile treats as zero-diff (identity with the new
	// config), masking any changes made between cold-start and the first
	// reconcile.
	provSnap, err := yaml.Marshal(cfg)
	if err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "marshal provision snapshot")
	}
	if err := WriteSnapshot(d.Tart, vmName, snapshotHeader+string(provSnap)); err != nil {
		return d.teardownOnFail(ctx, cfg, vmName, err, "write provision snapshot")
	}
	log.Printf("shell: snapshot written: %s", vmName)

	// Seed the daemon-side state snapshot too, now that provisioning is
	// fully green (RunBundle, RunUser, egress enforcement, and RunEnforced
	// all succeeded). Best-effort: log but don't fail — a missing snapshot
	// only degrades to "full diff on next reconcile" (safe), and failing
	// here would kill a start that otherwise succeeded.
	templateContents, err := render.RenderTemplatesByBasename(cfg, repoRoot, d.Ident.RuntimeDir(), cfg.Project.Name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "state: render templates for seed snapshot %s failed: %v\n", cfg.Project.Name, err)
	}
	snap := serviceapi.StateSnapshot{
		Cfg:              cfg,
		TemplateContents: templateContents,
		SecretHashes:     SecretHashesFromBindings(bindings),
		ProxyVersion:     ironproxy.EmbeddedSha256(), // stamp the version that just provisioned
		ProjectIP:        projectIP,
		MacCwd:           repoRoot,
	}
	if err := serviceapi.WriteStateSnapshot(d.Ident, cfg.Project.Name, snap); err != nil {
		fmt.Fprintf(os.Stderr, "state: seed snapshot for %s failed: %v\n", cfg.Project.Name, err)
	}

	// Emit the ssh_config Host block so `ssh devm-<name>` resolves for this
	// project. Mutagen's own sync transport is tart exec now (see
	// cmd/tart-mutagen-ssh) and has no dependency on this file — mutagen
	// setup already ran, above, before this point.
	if err := EmitSSHConfig(ctx, d.Ident, d.Tart); err != nil {
		log.Printf("ssh_config emit failed after provisioning: %v", err)
	}

	reporter.Step("ready", false)
	reporter.Stop()
	reporter.Clear()

	return d.attachShell(ctx, vmName, repoRoot, cmdName, cmdArgs)
}

// volumeSyncFn is the test-injection seam for the volume-sync stage
// provisionAndAttach runs after BeginProvisioning and before
// repoCloneFn. Production always dispatches to the daemon's
// /vm/volume-sync via d.ServiceAPIClient.VolumeSync; tests substitute a
// fake to verify sequencing without needing a live VM or the real
// mutagen binary.
var volumeSyncFn = func(d ShellDeps, ctx context.Context, cfg schema.Config, vmName, repoRoot string) error {
	return d.ServiceAPIClient.VolumeSync(ctx, vmName, cfg, repoRoot)
}

// repoCloneFn is the test-injection seam for the repo-clone stage
// provisionAndAttach runs after volumeSyncFn and before
// waitForInitialSyncFn. Production always dispatches to the daemon's
// /vm/repo-clone via d.ServiceAPIClient.RepoClone; tests substitute a
// fake to verify sequencing without a live VM.
var repoCloneFn = func(d ShellDeps, ctx context.Context, cfg schema.Config, vmName, repoRoot string, tunnelPort int) error {
	return d.ServiceAPIClient.RepoClone(ctx, vmName, cfg, repoRoot, tunnelPort)
}

// waitForInitialSyncFn is the test-injection seam for the wait-for-sync
// step provisionAndAttach runs immediately after repoCloneFn and before
// prov.RunUser. Production always calls (ShellDeps).waitForInitialSync;
// tests substitute a fake to verify sequencing without a live mutagen
// daemon.
var waitForInitialSyncFn = func(d ShellDeps, ctx context.Context, cfg schema.Config, vmName string) error {
	return d.waitForInitialSync(ctx, cfg, vmName)
}

// waitForInitialSync blocks until every one of vmName's mutagen sync
// sessions finishes converging, so RunUser's install:/startup: steps see a
// fully hydrated workspace rather than a still-syncing one.
func (d ShellDeps) waitForInitialSync(ctx context.Context, cfg schema.Config, vmName string) error {
	mutagenBin, err := mutagen.Ensure(d.Ident.RuntimeDir())
	if err != nil {
		return fmt.Errorf("mutagen: extract binary: %w", err)
	}
	cli := &mutagen.CLI{
		Binary:  mutagenBin,
		DataDir: filepath.Join(d.Ident.RuntimeDir(), "mutagen", "data"),
	}
	return serviceapi.FlushAll(cli, vmName)
}

// runStartupCommandsFn is the test-injection seam for the RunStartupCommands
// phase provisionAndAttach runs after prov.RunUser and before
// EndProvisioning — the workspace is already hydrated (volumeSyncFn +
// repoCloneFn + waitForInitialSyncFn ran before prov.RunUser) and the
// egress authority is still passthrough. Production always calls
// (ShellDeps).runStartupCommands; tests substitute a fake to verify
// sequencing without needing a live VM or the real mutagen binary.
var runStartupCommandsFn = func(d ShellDeps, ctx context.Context, cfg schema.Config, vmName, repoRoot string) error {
	return d.runStartupCommands(ctx, cfg, vmName, repoRoot)
}

// runStartupCommands invokes `run <name>` in each repo's guest cwd for
// every command with `startup: true`. Runs under the passthrough egress
// authority (before EndProvisioning) — a startup command isn't limited
// to the project's network.allow: list for this window. The workspace is
// already hydrated and flushed by volumeSyncFn/repoCloneFn/
// waitForInitialSyncFn upstream, so no re-flush is needed here.
//
// A non-zero exit fails cold-start (loud, teardown-class) — the user opted
// in via startup: true; silent failure defeats the point.
func (d ShellDeps) runStartupCommands(ctx context.Context, cfg schema.Config, vmName, repoRoot string) error {
	startupCmds := cfg.StartupCommands(repoRoot)
	if len(startupCmds) == 0 {
		return nil
	}

	return dispatchStartupCommands(d.guestExec(ctx, vmName), vmName, startupCmds)
}

// dispatchStartupCommands is the RunStartupCommands phase's core logic,
// factored out of (ShellDeps).runStartupCommands so unit tests can inject a
// fake guestExec without a live VM.
func dispatchStartupCommands(exec serviceapi.GuestExec, vmName string, startupCmds []schema.StartupCommand) error {
	log.Printf("shell: running startup commands: %s (%d)", vmName, len(startupCmds))
	// Progress marker consumed by newProvisionProgress in provisionAndAttach.
	fmt.Fprintln(os.Stderr, "::devm:stage:commands::")

	for _, cmd := range startupCmds {
		fmt.Fprintf(os.Stderr, "::devm:progress:commands:%s:%s::\n", cmd.Repo, cmd.Name)
		// Runs as the devm user (guestExec's script already runs as root via
		// `sudo bash -s`; sudo -u devm here drops to the devm user so PATH /
		// HOME resolve the same way an interactive `run` invocation would
		// see them).
		script := fmt.Sprintf(
			"sudo -u devm -H bash -c 'cd %s && %s /usr/local/bin/run %s'",
			shellSingleQuoted(cmd.GuestCwd), devmbundle.GuestWrapper, shellSingleQuoted(cmd.Name),
		)
		stdout, stderr, exitCode, err := exec(script)
		if err != nil {
			return fmt.Errorf("run %s/%s: exec: %w", cmd.Repo, cmd.Name, err)
		}
		if exitCode != 0 {
			return fmt.Errorf("run %s/%s: exit %d: %s\n%s",
				cmd.Repo, cmd.Name, exitCode, stderr, stdout)
		}
	}
	return nil
}

// shellSingleQuoted wraps s in single quotes for use as a bash literal.
// Duplicated from render.shellSingleQuoted (unexported there) rather than
// exporting it — the orchestrator has no other reason to depend on render's
// internals.
func shellSingleQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// guestExec returns a serviceapi.GuestExec that runs a script inside
// vmName via `tart exec -i <name> sudo bash -s`, script on stdin. Scripts
// embed their own `sudo -u devm` where they need to drop privileges.
// Routed through d.Tart (rather than a raw exec.Command) so tests can
// substitute the same fake tart binary they already use for RunBundle/
// RunUser/RunEnforced.
func (d ShellDeps) guestExec(ctx context.Context, vmName string) serviceapi.GuestExec {
	return func(script string) (stdout, stderr string, exitCode int, err error) {
		res := d.Tart.ExecStdin(ctx, vmName, strings.NewReader(script), []string{"sudo", "bash", "-s"})
		return res.Stdout, res.Stderr, res.ExitCode, nil
	}
}

// defaultInstallStepTimeoutSeconds is installStepTimeoutSeconds' fallback
// when DEVM_INSTALL_STEP_TIMEOUT_S is unset or invalid. Matches
// render.defaultStepTimeoutSeconds.
const defaultInstallStepTimeoutSeconds = 600

// installStepTimeoutSeconds reads DEVM_INSTALL_STEP_TIMEOUT_S — the e2e
// suite's override for the composed script's install:/startup: `timeout`
// budget — falling back to defaultInstallStepTimeoutSeconds when the var is
// unset or not a positive integer. Mirrors the old per-step provisioner's
// os.Getenv("DEVM_INSTALL_STEP_TIMEOUT_S") handling.
func installStepTimeoutSeconds() int {
	v := os.Getenv("DEVM_INSTALL_STEP_TIMEOUT_S")
	if v == "" {
		return defaultInstallStepTimeoutSeconds
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultInstallStepTimeoutSeconds
	}
	return n
}

// teardownVM stops and deletes vmName via the daemon + tart. Used both by
// teardownOnFail (a provisioning-time failure) and directly by RunShell
// when it finds the VM in a dirty (interrupted-provisioning) state and
// must destroy it before a fresh cold start — never provision onto a
// dirty slate.
func (d ShellDeps) teardownVM(ctx context.Context, cfg schema.Config, vmName string) error {
	teardownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// destroy: true — teardownVM always precedes a tart.Delete of this
	// VM's disk below, so the daemon should purge this project's egress
	// policy state and denial counts too.
	if stopErr := d.ServiceAPIClient.StopVM(teardownCtx, cfg.Project.Name, true); stopErr != nil {
		// StopVM is best-effort here (VM may be already stopped or gone),
		// but if it errored for a reason worth diagnosing, we want the
		// caller to see it — otherwise this class of failure (VM stopped
		// but not deleted) is invisible from the outside.
		fmt.Fprintf(os.Stderr, "teardown: StopVM: %v\n", stopErr)
		log.Printf("shell: teardown: StopVM: %v", stopErr)
	}
	if derr := d.Tart.Delete(teardownCtx, vmName); derr != nil &&
		!strings.Contains(derr.Error(), "does not exist") {
		fmt.Fprintf(os.Stderr, "teardown: tart delete %s failed: %v\n", vmName, derr)
		log.Printf("shell: teardown: delete %s failed: %v", vmName, derr)
		return fmt.Errorf("tart delete %s: %w", vmName, derr)
	}
	return nil
}

// teardownOnFail tears down vmName via teardownVM and wraps err/msg into
// the (int, error) shape RunShell/provisionAndAttach return. Any
// cold-start-style failure (pre-service-start) must tear down the VM to
// avoid leaving a zombie — `devm shell` promises loud-failure: exit
// non-zero AND leave no half-created VM behind (pinned by test_51).
func (d ShellDeps) teardownOnFail(ctx context.Context, cfg schema.Config, vmName string, err error, msg string) (int, error) {
	log.Printf("shell: failed: %s: %v", msg, err)
	fmt.Fprintf(os.Stderr, "teardown-on-fail: %s: %v\n", msg, err)

	// Best-effort flush of mutagen sessions before destroying the VM. Under
	// the new lifecycle mutagen sync runs before install:/startup:, so if
	// those user phases write to $WORKSPACE and then fail, the writes only
	// reach the Mac mirror if mutagen's watcher happens to sync them before
	// we destroy the guest-side workspace here. Explicit flush closes that
	// race — any in-flight writes propagate to Mac before teardown.
	//
	// Failures here don't block teardown (we're already in a failure path);
	// just log.
	if ferr := flushMutagenOnTeardownFn(d, ctx, cfg, vmName); ferr != nil {
		log.Printf("shell: teardown flush mutagen failed (continuing): %v", ferr)
	}

	if terr := d.teardownVM(ctx, cfg, vmName); terr != nil {
		fmt.Fprintf(os.Stderr, "teardown-on-fail: %v\n", terr)
	}
	return -1, fmt.Errorf("%s: %w", msg, err)
}

// flushMutagenOnTeardownFn is the test-injection seam for teardownOnFail's
// pre-destroy mutagen flush. Production always calls
// (ShellDeps).flushMutagenOnTeardown; tests substitute a fake to verify
// call ordering without a live mutagen daemon.
var flushMutagenOnTeardownFn = func(d ShellDeps, ctx context.Context, cfg schema.Config, vmName string) error {
	return d.flushMutagenOnTeardown(ctx, cfg, vmName)
}

// flushMutagenOnTeardown builds the mutagen CLI the same way
// waitForInitialSync does and flushes all of vmName's sync sessions. See
// teardownOnFail for why this runs before the VM is destroyed.
func (d ShellDeps) flushMutagenOnTeardown(ctx context.Context, cfg schema.Config, vmName string) error {
	mutagenBin, err := mutagen.Ensure(d.Ident.RuntimeDir())
	if err != nil {
		return fmt.Errorf("mutagen: extract binary: %w", err)
	}
	cli := &mutagen.CLI{
		Binary:  mutagenBin,
		DataDir: filepath.Join(d.Ident.RuntimeDir(), "mutagen", "data"),
	}
	return serviceapi.FlushAll(cli, vmName)
}

// attachShell attaches an interactive shell inside the VM via `tart exec`.
// The tart binary is invoked via UserSpawner so the user's terminal
// stdin/stdout/stderr are inherited (ExecSpawner with Interactive=true).
//
// `tart exec` defaults to non-interactive: no stdin attached, no PTY
// allocated. When the caller's stdin is itself a TTY (a real terminal
// or pexpect), pass `-i -t` so bash sees a TTY and stays interactive
// instead of exiting on EOF.
//
// The user command is invoked via the with-devm-env wrapper so the
// project env (/etc/environment) is sourced before argv runs. The wrapper
// lives in the guest at devmbundle.GuestWrapper, installed by the
// provisioner's "install devm bundle" step.
func (d ShellDeps) attachShell(ctx context.Context, vmName, repoRoot, cmdName string, cmdArgs []string) (int, error) {
	execArgs := []string{"exec"}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		execArgs = append(execArgs, "-i", "-t")
	}
	wrapper := devmbundle.GuestWrapper
	execArgs = append(execArgs, vmName)
	// Forward host terminal env into the guest so TUIs see the real
	// TERM (colors, keybindings, TUI capabilities). tart exec has no
	// --env flag, so we chain through env(1) inside the argv. Same
	// semantic the old sbx `-e KEY=VAL` block had; the tart migration
	// (c97bcc2) dropped it and colors regressed.
	execArgs = append(execArgs, terminalEnvForward()...)
	execArgs = append(execArgs, wrapper, cmdName)
	execArgs = append(execArgs, cmdArgs...)
	log.Printf("shell: attaching interactive shell: tart exec %s %v", vmName, redactedExecArgs(execArgs))
	cmd, err := d.UserSpawner.Start(d.Tart.Path, execArgs...)
	if err != nil {
		return -1, fmt.Errorf("spawn interactive shell: %w", err)
	}
	rc, err := cmd.Wait()
	if err != nil {
		return -1, fmt.Errorf("interactive shell wait: %w", err)
	}
	return rc, nil
}

// waitVMReady polls `tart exec <vmName> true` until exit 0 or timeout.
// Each attempt is bounded by perAttemptTimeout so a single hung
// `tart exec` call (which can happen under system load when the guest
// agent socket is slow to respond) doesn't consume the whole budget
// and drop the effective retry count. Without this bound, we've seen
// 60s deadlines silently used up by 3-4 slow attempts instead of ~60.
func waitVMReady(ctx context.Context, tr *tart.Tart, vmName string, timeout time.Duration) error {
	const perAttemptTimeout = 3 * time.Second
	deadline := time.Now().Add(timeout)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		r := tr.Exec(attemptCtx, vmName, []string{"true"})
		cancel()
		if r.ExitCode == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return fmt.Errorf(
		"timeout waiting for vm %s to become exec-ready (%d attempts over %s)",
		vmName, attempt, timeout,
	)
}

// caStorageDir returns the ca/ directory alongside the socket,
// consistent with Ship 3's CA location.
func caStorageDir(cfg identity.Config) string {
	return filepath.Join(cfg.RuntimeDir(), "ca")
}
