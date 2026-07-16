// Package provision orchestrates per-project first-boot work in a
// freshly cloned Tart VM. The orchestrator (Task 9) calls
// Provisioner.Run after tart run + supervisor.Spawn succeed.
package provision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/nftscript"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
)

// tartExecer is the subset of *tart.Tart used by Provisioner. Defined as
// an interface so tests can inject fakes without shelling out to tart.
type tartExecer interface {
	Exec(ctx context.Context, name string, argv []string) tart.ExecResult
	ExecWithRetry(ctx context.Context, name string, argv []string) tart.ExecResult
	ExecStdin(ctx context.Context, name string, stdin io.Reader, argv []string) tart.ExecResult
}

// Provisioner runs the per-project first-boot sequence in a Tart VM.
type Provisioner struct {
	Tart   tartExecer
	VMName string
	Cfg    schema.Config

	// CARootPEM is the contents of ~/Library/Application Support/devm/ca/root.crt.
	// Threaded into devmbundle.Build's BuildInput so install.sh inside the
	// guest installs it into /usr/local/share/ca-certificates/devm.crt and
	// merges it into the trusted bundle via update-ca-certificates.
	CARootPEM []byte

	// SSHAuthorizedPubkey is the ssh-ed25519 pubkey line devm writes into
	// the guest's ~devm/.ssh/authorized_keys.
	SSHAuthorizedPubkey []byte

	// SSHHostPriv/Pub are the guest's own host key material. Persistent
	// across VM recreates via ~/Library/Application Support/devm/ssh/projects/<id>/.
	SSHHostPriv []byte
	SSHHostPub  []byte

	// WorkspaceVMPath is the path inside the VM where the workspace
	// is mounted. Mirrored paths (Ship 4 decision): the same path as
	// on the Mac (e.g., /Users/michael/projects/myproj).
	WorkspaceVMPath string

	// EnforceEgress is called between "systemctl daemon-reload" and
	// "enable + start services". It asks the daemon to inject the iron-
	// proxy nftables + dnsmasq scripts inside the VM, so systemd services
	// come up UNDER enforcement while the earlier install: / apt-get /
	// template-install steps ran with open network. Nil is allowed —
	// tests that don't need enforcement (unit-tests, non-daemon paths)
	// leave it unset.
	EnforceEgress func(context.Context) error

	// firstBoot is true when /var/lib/devm/provisioned is absent — i.e.
	// this VM has not completed provisioning. Set at the top of Run.
	firstBoot bool
}

// StepFailure carries which provisioning step failed. Callers use this
// to decide whether the VM is worth keeping (service startup failure →
// user can debug in-place) or should be torn down (install-phase failure
// → the VM is in a bad state and the user's fix belongs in devm.yaml).
type StepFailure struct {
	Step string
	Err  error
}

func (f *StepFailure) Error() string {
	return fmt.Sprintf("provision step %q: %v", f.Step, f.Err)
}

func (f *StepFailure) Unwrap() error { return f.Err }

// stepsAfterInstall are the steps that come AFTER "run install commands".
// A failure at or after any of these is considered post-install: the VM
// is basically good, the user's service definition is what's broken, and
// devm shell should surface the error but leave the VM running so the
// user can `tart exec` in and inspect. Anything before this list (install
// commands, apt-get, CA install, mounts, etc.) is a cold-start-broken
// state where the VM is worth destroying and re-creating from scratch.
var stepsAfterInstall = map[string]bool{
	"install templates":       true,
	"systemctl daemon-reload": true,
	"enable + start services": true,
	"apply masks":             true,
	"write first-boot marker": true,
}

// IsPostInstallFailure reports whether err is a StepFailure at or after
// the "install templates" step — i.e. a failure that leaves the VM in
// a debuggable state and shouldn't trigger teardown.
func IsPostInstallFailure(err error) bool {
	var sf *StepFailure
	if !errors.As(err, &sf) {
		return false
	}
	return stepsAfterInstall[sf.Step]
}

// Run executes the full provisioning sequence. Streams progress and
// per-step output to w. Returns on the first failure.
//
// Each step's output is prefixed with [step: <name>] so failures
// point clearly. The returned error is always a *StepFailure so callers
// can classify install-phase vs service-phase failures via
// IsPostInstallFailure.
func (p *Provisioner) Run(ctx context.Context, w io.Writer) error {
	p.firstBoot = !p.markerExists(ctx)
	steps := []struct {
		name          string
		firstBootOnly bool
		fn            func(context.Context, io.Writer) error
	}{
		{name: "mkdir workspace parents", fn: p.mkdirWorkspaceParents},
		{name: "install devm bundle", fn: p.installDevmBundle},
		{name: "reload base services", fn: p.reloadBaseServices},
		{name: "apt-get update", firstBootOnly: true, fn: p.aptUpdate},
		{name: "apt-get install packages", firstBootOnly: true, fn: p.aptInstall},
		{name: "run install commands", firstBootOnly: true, fn: p.runInstallCommands},
		{name: "docker feature", firstBootOnly: true, fn: p.dockerFeature},
		{name: "install templates", fn: p.installTemplates},
		{name: "systemctl daemon-reload", fn: p.daemonReload},
		{name: "apply egress enforcement", fn: p.applyEgressEnforcement},
		{name: "apply svc_ingress firewall", fn: p.applySvcIngressFirewall},
		{name: "enable + start services", fn: p.enableStartServices},
		{name: "apply masks", fn: p.applyMasks},
	}
	for _, step := range steps {
		if step.firstBootOnly && !p.firstBoot {
			fmt.Fprintf(w, "\n[step: %s] (skipped — already provisioned)\n", step.name)
			continue
		}
		fmt.Fprintf(w, "\n[step: %s]\n", step.name)
		if err := step.fn(ctx, w); err != nil {
			return &StepFailure{Step: step.name, Err: err}
		}
	}
	if p.firstBoot {
		if err := p.writeMarker(ctx, w); err != nil {
			return &StepFailure{Step: "write first-boot marker", Err: err}
		}
	}
	return nil
}

// provisionedMarker is the path in the guest whose presence indicates
// this VM has already completed first-boot provisioning.
const provisionedMarker = "/var/lib/devm/provisioned"

// markerExists reports whether the first-boot marker is present in the guest.
func (p *Provisioner) markerExists(ctx context.Context) bool {
	return p.Tart.Exec(ctx, p.VMName, []string{"test", "-f", provisionedMarker}).ExitCode == 0
}

// writeMarker records that first-boot provisioning completed.
func (p *Provisioner) writeMarker(ctx context.Context, w io.Writer) error {
	return p.execShell(ctx, w, "sudo mkdir -p /var/lib/devm && sudo touch "+provisionedMarker)
}

// exec runs the given argv via tart.ExecWithRetry (defends against
// transient tart-guest-agent transport drops mid-provisioning), writes
// captured stdout + stderr to w, and returns an error if exit code is
// nonzero.
func (p *Provisioner) exec(ctx context.Context, w io.Writer, argv ...string) error {
	r := p.Tart.ExecWithRetry(ctx, p.VMName, argv)
	if r.Stdout != "" {
		_, _ = io.WriteString(w, r.Stdout)
	}
	if r.Stderr != "" {
		_, _ = io.WriteString(w, r.Stderr)
	}
	if r.ExitCode != 0 {
		return fmt.Errorf("tart exec %s: exit %d", strings.Join(argv, " "), r.ExitCode)
	}
	return nil
}

// execShell runs the given shell script via `bash -c "..."` for steps
// that need pipes, redirection, or compound commands.
func (p *Provisioner) execShell(ctx context.Context, w io.Writer, script string) error {
	// -o errexit + -o pipefail so any pipeline component failing (not just
	// the last) aborts the script. -o nounset would be nice but many user
	// install steps rely on unset-vars-as-empty (e.g., ${FOO:-default}).
	return p.exec(ctx, w, "bash", "-e", "-o", "pipefail", "-c", script)
}

// ExecShell is the exported entrypoint the docker package calls; wraps
// the internal execShell.
func (p *Provisioner) ExecShell(ctx context.Context, w io.Writer, script string) error {
	return p.execShell(ctx, w, script)
}

// PipeIntoShell pipes stdin into a shell script running inside the VM.
// Used for delivering payloads too large for a single tart-exec argv
// (e.g., embedded binaries via `sudo tee <path>`).
func (p *Provisioner) PipeIntoShell(ctx context.Context, w io.Writer, stdin io.Reader, script string) error {
	argv := []string{"bash", "-e", "-o", "pipefail", "-c", script}
	r := p.Tart.ExecStdin(ctx, p.VMName, stdin, argv)
	if r.Stdout != "" {
		_, _ = io.WriteString(w, r.Stdout)
	}
	if r.Stderr != "" {
		_, _ = io.WriteString(w, r.Stderr)
	}
	if r.ExitCode != 0 {
		return fmt.Errorf("tart exec -i %s: exit %d", strings.Join(argv, " "), r.ExitCode)
	}
	return nil
}

func (p *Provisioner) mkdirWorkspaceParents(ctx context.Context, w io.Writer) error {
	parent := filepath.Dir(p.WorkspaceVMPath)
	return p.exec(ctx, w, "sudo", "mkdir", "-p", parent)
}

// installDevmBundle builds the devm-owned artifact bundle (env file,
// with-devm-env wrapper, install-templates.sh dispatcher, per-template
// installers, install.sh) and pipes it into the guest, where install.sh
// extracts it to /opt/devm and symlinks with-devm-env onto PATH. Runs
// early — before "run install commands" and the docker feature — so
// every later step that needs the wrapper finds it.
func (p *Provisioner) installDevmBundle(ctx context.Context, w io.Writer) error {
	in := devmbundle.BuildInput{
		Cfg:                 p.Cfg,
		RepoRoot:            p.WorkspaceVMPath,
		CARootPEM:           p.CARootPEM,
		SSHAuthorizedPubkey: p.SSHAuthorizedPubkey,
		SSHHostPriv:         p.SSHHostPriv,
		SSHHostPub:          p.SSHHostPub,
	}
	if p.Cfg.Docker {
		in.DockerRuncShim = docker.Shim()
		in.DockerCLIShim = docker.DockerShim()
	}
	body, err := devmbundle.Build(in)
	if err != nil {
		return fmt.Errorf("build devm bundle: %w", err)
	}
	return p.PipeIntoShell(ctx, w, bytes.NewReader(body), devmbundle.GuestInstallScript)
}

func (p *Provisioner) applyEgressEnforcement(ctx context.Context, w io.Writer) error {
	if p.EnforceEgress == nil {
		fmt.Fprintln(w, "(no EnforceEgress callback set — skipping)")
		return nil
	}
	return p.EnforceEgress(ctx)
}

// applySvcIngressFirewall flush-rebuilds the svc_ingress nftables chain
// from the project's direct-service ports, so Mac→container traffic for
// `direct: true` published ports passes the forward hook's policy-drop.
// Runs after applyEgressEnforcement — which is what scaffolds the
// `jump svc_ingress` into the forward chain at cold-start — so the chain
// this populates is already wired in. Skipped entirely for non-docker /
// no-direct-service projects: DirectPorts returns nil and there's nothing
// to open.
func (p *Provisioner) applySvcIngressFirewall(ctx context.Context, w io.Writer) error {
	ports := nftscript.DirectPorts(p.Cfg)
	if len(ports) == 0 {
		fmt.Fprintln(w, "(no direct docker services — skipping)")
		return nil
	}
	return p.execShell(ctx, w, nftscript.BuildSvcIngressScript(ports))
}

func (p *Provisioner) reloadBaseServices(ctx context.Context, w io.Writer) error {
	// caddy: reload-or-restart handles config change + first start.
	if err := p.exec(ctx, w, "sudo", "systemctl", "reload-or-restart", "caddy"); err != nil {
		return err
	}
	// ssh: the bundle install.sh already unmasked; enable+start turns
	// it on now that per-project host key + authorized_keys are in place.
	if err := p.exec(ctx, w, "sudo", "systemctl", "enable", "--now", "ssh"); err != nil {
		return err
	}
	// Dnsmasq stays deferred — see applyEgressEnforcement (still holds
	// :53 via systemd-resolved until the egress step masks it).
	return nil
}

func (p *Provisioner) aptUpdate(ctx context.Context, w io.Writer) error {
	// No packages declared → no point fetching the index. Skipping
	// is also necessary under Ship 5: deb.debian.org isn't typically
	// in the project's allow-list, so apt-get update would either
	// hang on blocked DNS or fail outright once iron-proxy enforces.
	if len(p.Cfg.Packages) == 0 {
		fmt.Fprintln(w, "(no packages declared, skipping)")
		return nil
	}
	return p.exec(ctx, w, "sudo", "apt-get", "update", "-y")
}

func (p *Provisioner) aptInstall(ctx context.Context, w io.Writer) error {
	if len(p.Cfg.Packages) == 0 {
		fmt.Fprintln(w, "(no packages declared)")
		return nil
	}
	args := append([]string{"sudo", "apt-get", "install", "-y"}, p.Cfg.Packages...)
	return p.exec(ctx, w, args...)
}

const defaultInstallStepTimeout = 600 * time.Second

func (p *Provisioner) runInstallCommands(ctx context.Context, w io.Writer) error {
	if len(p.Cfg.Install) == 0 {
		fmt.Fprintln(w, "(no install commands)")
		return nil
	}
	budget := defaultInstallStepTimeout
	if v := os.Getenv("DEVM_INSTALL_STEP_TIMEOUT_S"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			budget = time.Duration(n) * time.Second
		}
	}
	// Install commands run through the with-devm-env wrapper so the
	// user's project env (WORKSPACE_DIR, path: entries, cfg.Env values)
	// is sourced from .devm/.env before their command runs. Same wrapper
	// as the interactive shell path in orchestrator/shell.go.
	wrapper := devmbundle.GuestWrapper
	for i, command := range p.Cfg.Install {
		fmt.Fprintf(w, "[%d/%d] %s\n", i+1, len(p.Cfg.Install), command)
		stepCtx, cancel := context.WithTimeout(ctx, budget)
		err := p.exec(stepCtx, w, wrapper, "bash", "-e", "-o", "pipefail", "-c", command)
		cancel()
		if errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("install step %d (%q) timed out after %ds",
				i+1, command, int(budget.Seconds()))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// dockerFeature installs Docker Engine + devm-runc-shim + firewall rule
// inside the VM when the project's devm.yaml declares `docker: true`.
// No-op otherwise. The 15-minute deadline covers `curl get.docker.com | sh`
// fetching upstream packages on a cold cache.
func (p *Provisioner) dockerFeature(ctx context.Context, w io.Writer) error {
	if !p.Cfg.Docker {
		fmt.Fprintln(w, "(docker: false — skipping)")
		return nil
	}
	stepCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	return docker.Install(stepCtx, w, p)
}

// installTemplates runs the install-templates.sh dispatcher inside the VM,
// which loops over /opt/devm/templates/*.sh and executes each per-template
// installer. Each installer is idempotent (atomic rename over the target
// path) so re-running on warm restart is safe.
//
// No-op when no templates are declared (empty /opt/devm/templates/ dir
// causes the dispatcher to exit 0 immediately).
//
// Runs THROUGH with-devm-env for the auto-cd-to-$WORKSPACE + terminfo
// setup it provides; the dispatcher itself reads a fixed /opt/devm path.
func (p *Provisioner) installTemplates(ctx context.Context, w io.Writer) error {
	anyTemplate := false
	for _, svc := range p.Cfg.Services {
		if len(svc.Templates) > 0 {
			anyTemplate = true
			break
		}
	}
	if !anyTemplate {
		fmt.Fprintln(w, "(no templates declared)")
		return nil
	}
	wrapper := devmbundle.GuestWrapper
	dispatcher := devmbundle.GuestDispatcher
	return p.exec(ctx, w, wrapper, "bash", dispatcher)
}

func (p *Provisioner) daemonReload(ctx context.Context, w io.Writer) error {
	return p.exec(ctx, w, "sudo", "systemctl", "daemon-reload")
}

const (
	healthPollInterval = 500 * time.Millisecond
	healthTotalBudget  = 10 * time.Second
)

func (p *Provisioner) enableStartServices(ctx context.Context, w io.Writer) error {
	// Collect non-routing-only services (skipping ones with no Exec + no
	// Systemd — those are proxy-routing declarations with no in-VM process).
	// Split by lifecycle: long-running services need `active`; one-shot
	// services (Restart == "no") ran-to-completion means `inactive` is OK
	// and only `failed` counts as a failure.
	type entry struct {
		name    string
		oneShot bool
	}
	var entries []entry
	for name, svc := range p.Cfg.Services {
		if svc.Systemd == "" && len(svc.Exec) == 0 {
			fmt.Fprintf(w, "(skip %s — routing-only declaration)\n", name)
			continue
		}
		entries = append(entries, entry{name: name, oneShot: svc.Restart == "no"})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	for _, e := range entries {
		unitName := e.name + ".service"
		if err := p.exec(ctx, w, "sudo", "systemctl", "enable", "--now", unitName); err != nil {
			return err
		}
	}

	// Poll each service. Long-running: wait for `active`. One-shot: wait
	// for a terminal state that isn't `failed` (usually `inactive`).
	deadline := time.Now().Add(healthTotalBudget)
	for _, e := range entries {
		for {
			r := p.Tart.Exec(ctx, p.VMName, []string{"systemctl", "is-active", e.name})
			state := strings.TrimSpace(r.Stdout)
			if state == "failed" {
				return fmt.Errorf("service %q did not become active: status=%s", e.name, state)
			}
			if e.oneShot {
				if state == "inactive" || r.ExitCode == 0 {
					break
				}
			} else if r.ExitCode == 0 {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("service %q did not become active: status=%s (timeout)", e.name, state)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(healthPollInterval):
			}
		}
	}
	return nil
}

func (p *Provisioner) applyMasks(ctx context.Context, w io.Writer) error {
	for svcName, svc := range p.Cfg.Services {
		// Chown to the user the service will run as (default devm). Without
		// this the mask dir stays root-owned from `sudo mkdir` and a
		// non-root service can't write into its own mask. Same default as
		// render.RenderService's User=.
		owner := svc.User
		if owner == "" {
			owner = "devm"
		}
		for _, m := range svc.Masks {
			maskHostPath := filepath.Join("/var/devm/masks",
				p.Cfg.Project.Name, svcName, m.Path)
			mountTarget := filepath.Join(p.WorkspaceVMPath, m.Path)
			script := strings.Join([]string{
				"sudo", "mkdir", "-p", maskHostPath, "&&",
				"sudo", "chown", owner, maskHostPath, "&&",
				"sudo", "mkdir", "-p", mountTarget, "&&",
				"sudo", "mount", "--bind", maskHostPath, mountTarget,
			}, " ")
			if err := p.execShell(ctx, w, script); err != nil {
				return err
			}
		}
	}
	return nil
}
