// Package provision composes and ships the guest provisioning scripts for a
// Tart VM, split into two execs around the daemon's softnet OPEN→ENFORCED
// egress flip: RunOpen (render.RenderProvisionOpenScript, with the devm
// bundle tar on stdin) runs while egress is OPEN, and RunEnforced (render.
// RenderProvisionEnforcedScript, which starts user services and activates
// devm.target) runs after the orchestrator has flipped softnet to ENFORCED —
// so services never come up under open egress. Each script's exit code is
// that half's provisioning result.
package provision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/guestbin"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
)

// tartExecer is the subset of *tart.Tart used by Provisioner. Defined as
// an interface so tests can inject fakes without shelling out to tart.
type tartExecer interface {
	ExecWithRetry(ctx context.Context, name string, argv []string) tart.ExecResult
	ExecStream(ctx context.Context, name string, stdin io.Reader, argv []string, onLine func(stream, line string)) (int, error)
}

// Provisioner ships the composed provisioning script to a Tart VM.
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

	// MacCwd is the project's Mac-side working directory — used to read
	// the effective git user.name/user.email (git's own precedence:
	// repo-local > global > system) to mirror into the guest .gitconfig.
	// Empty ⇒ no identity read is attempted (see gitIdentity).
	MacCwd string

	// DaemonRuntimeDir is the daemon's runtime directory (identity.
	// Config.RuntimeDir()), threaded into devmbundle.Build's BuildInput
	// as the root of RenderTemplates' scratch namespace.
	DaemonRuntimeDir string

	// StepTimeoutSeconds bounds every install:/startup: command in the
	// composed script (render.ProvisionScriptInput.StepTimeoutSeconds). The
	// daemon fills this from DEVM_INSTALL_STEP_TIMEOUT_S; zero means
	// "unset" and RenderProvisionOpenScript falls back to its own default.
	StepTimeoutSeconds int

	// PackageAdds / PackageRemoves are the apt package drift to converge
	// on a non-first boot (render.ProvisionScriptInput.PackageAdds/
	// PackageRemoves) — pass-through from the caller, which computes them
	// against the last-applied state snapshot.
	PackageAdds    []string
	PackageRemoves []string

	// firstBoot is true when /var/lib/devm/provisioned is absent — i.e.
	// this VM has not completed provisioning. Set at the top of Run.
	firstBoot bool
}

// StepFailure carries which provisioning stage the script had reached when
// it failed. Callers use this to decide whether the VM is worth keeping
// (service-phase failure → the user's service definition is broken, debug
// in-place) or should be torn down (install-phase failure → the VM is in a
// bad state and the user's fix belongs in devm.yaml).
type StepFailure struct {
	Step string
	Err  error
}

func (f *StepFailure) Error() string {
	step := f.Step
	if step == "" {
		step = "provision"
	}
	return fmt.Sprintf("provision stage %q: %v", step, f.Err)
}

func (f *StepFailure) Unwrap() error { return f.Err }

// stagesAfterInstall are the composed-script stages at or after which a
// failure is considered post-install: the VM is basically good and the
// user's service definition is what's broken, so `devm shell` should
// surface the error but leave the VM running for `tart exec` inspection.
// Any earlier stage (extract, open, apt, install:, docker, templates,
// startup:, enforce) is a cold-start-broken state where the VM is worth
// destroying and re-creating.
//
// templates deliberately does NOT keep the VM even though it runs after
// install:/docker: templates run in the composed script's OPEN (unenforced)
// egress window, before the enforce stage installs the real allowlist. A
// VM kept alive on a templates failure would be sitting there unenforced.
// services is the only kept-on-failure stage because it's the one stage
// that runs AFTER enforce.
var stagesAfterInstall = map[string]bool{
	"services": true,
}

// IsPostInstallFailure reports whether err is a StepFailure at a stage
// that leaves the VM in a debuggable state and shouldn't trigger teardown.
func IsPostInstallFailure(err error) bool {
	var sf *StepFailure
	if !errors.As(err, &sf) {
		return false
	}
	return stagesAfterInstall[sf.Step]
}

// RunOpen ships the OPEN-egress half of provisioning (render.
// RenderProvisionOpenScript) plus the bundle tar in ONE streaming `tart exec
// -i`, run while softnet is OPEN. It sets p.firstBoot (read by
// RunEnforced's scriptInput too) from the guest's first-boot marker. Every
// streamed line is written to w (diagnostic capture) and forwarded to
// onLine (stage-marker parsing / spinner — may be nil). The script's exit
// code is the whole result: a non-zero exit returns a *StepFailure
// classified by the last stage the script reached, so callers can
// distinguish install-phase from service-phase failures via
// IsPostInstallFailure.
func (p *Provisioner) RunOpen(ctx context.Context, w io.Writer, onLine func(stream, line string)) error {
	p.firstBoot = !p.markerExists(ctx)

	body, err := p.buildBundle()
	if err != nil {
		return &StepFailure{Step: "extract", Err: err}
	}

	script := render.RenderProvisionOpenScript(p.scriptInput())
	return p.execScript(ctx, script, bytes.NewReader(body), w, onLine)
}

// RunEnforced ships the ENFORCED-egress half of provisioning (render.
// RenderProvisionEnforcedScript) in ONE streaming `tart exec -i`, run
// immediately after softnet has been flipped to ENFORCED. No bundle is sent
// on stdin — /opt/devm was already extracted by RunOpen. Same
// StepFailure classification and streaming behavior as RunOpen. Callers
// must call RunOpen first so p.firstBoot is set from the guest's marker.
func (p *Provisioner) RunEnforced(ctx context.Context, w io.Writer, onLine func(stream, line string)) error {
	script := render.RenderProvisionEnforcedScript(p.scriptInput())
	return p.execScript(ctx, script, nil, w, onLine)
}

// execScript is the shared ExecStream + stage-classification plumbing
// RunOpen and RunEnforced both use.
func (p *Provisioner) execScript(ctx context.Context, script []byte, stdin io.Reader, w io.Writer, onLine func(stream, line string)) error {
	var st stageTracker
	wrapped := func(stream, line string) {
		st.observe(line)
		if w != nil {
			fmt.Fprintln(w, line)
		}
		if onLine != nil {
			onLine(stream, line)
		}
	}

	exit, err := p.Tart.ExecStream(ctx, p.VMName, stdin,
		[]string{"bash", "-c", string(script)}, wrapped)
	if err != nil {
		return &StepFailure{Step: st.current(), Err: err}
	}
	if exit != 0 {
		return &StepFailure{Step: st.current(), Err: fmt.Errorf("provisioning script exited %d", exit)}
	}
	return nil
}

// scriptInput assembles the ProvisionScriptInput from the project config.
func (p *Provisioner) scriptInput() render.ProvisionScriptInput {
	// Emit the gitconfig when either repo bindings exist OR the git
	// identity resolves — otherwise a bare workspace with an
	// identity-only config gets no /home/devm/.gitconfig at all.
	// .git-credentials still only ships when there are bindings.
	var creds, gitconfig string
	bindings := p.repoBindings()
	identity := p.gitIdentity()
	if len(bindings) > 0 || identity.UserName != "" || identity.UserEmail != "" {
		creds, gitconfig = render.RenderGitCredentials(bindings, identity)
	}
	return render.ProvisionScriptInput{
		FirstBoot:          p.firstBoot,
		Packages:           p.Cfg.Packages,
		Install:            p.Cfg.Install,
		Docker:             p.Cfg.Docker,
		InstallTemplates:   p.hasTemplates(),
		Startup:            p.Cfg.Startup,
		Scripts:            p.Cfg.Scripts,
		Services:           p.serviceUnits(),
		Masks:              p.maskMounts(),
		StepTimeoutSeconds: p.StepTimeoutSeconds,
		PackageAdds:        p.PackageAdds,
		PackageRemoves:     p.PackageRemoves,
		GitCredentials:     creds,
		GitConfig:          gitconfig,
	}
}

// gitIdentity reads the Mac-side effective git user.name/user.email for
// MacCwd via git's own config precedence (repo-local .git/config >
// ~/.gitconfig > /etc/gitconfig) — whatever git would use for a commit
// in that directory. p.MacCwd empty (unset in tests, or no project cwd
// known) skips the read entirely rather than falling back to this
// process's own cwd. Either lookup failing (not a repo, no identity
// configured anywhere) yields an empty string for that field — that's a
// normal, silent "unset", not an error.
func (p *Provisioner) gitIdentity() render.GitIdentity {
	if p.MacCwd == "" {
		return render.GitIdentity{}
	}
	name, _ := exec.Command("git", "-C", p.MacCwd, "config", "user.name").Output()
	email, _ := exec.Command("git", "-C", p.MacCwd, "config", "user.email").Output()
	return render.GitIdentity{
		UserName:  strings.TrimSpace(string(name)),
		UserEmail: strings.TrimSpace(string(email)),
	}
}

// repoBindings flattens p.Cfg's repo declarations (top-level + per-volume)
// into the order-stable slice RenderGitCredentials consumes. Primary
// first (when declared), then per-volume repos in sorted volume-name
// order. Volumes without a repo: block or without a resolved URL are
// skipped. A per-volume repo with an empty Secret inherits from the
// top-level secret (mirrors schema.Validate's inheritance rule).
func (p *Provisioner) repoBindings() []render.RepoBinding {
	var bindings []render.RepoBinding
	var topSecret string
	if p.Cfg.Repo != nil {
		topSecret = p.Cfg.Repo.Secret
		if p.Cfg.Repo.URL != nil && *p.Cfg.Repo.URL != "" {
			bindings = append(bindings, render.RepoBinding{
				URL: *p.Cfg.Repo.URL, Secret: p.Cfg.Repo.Secret,
			})
		}
	}
	names := make([]string, 0, len(p.Cfg.Volumes))
	for name := range p.Cfg.Volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		vol := p.Cfg.Volumes[name]
		if vol.Repo == nil || vol.Repo.URL == nil || *vol.Repo.URL == "" {
			continue
		}
		secret := vol.Repo.Secret
		if secret == "" {
			secret = topSecret
		}
		bindings = append(bindings, render.RepoBinding{
			URL: *vol.Repo.URL, Secret: secret,
		})
	}
	return bindings
}

// buildBundle builds the devm-owned artifact bundle tar (env file,
// with-devm-env wrapper, install-templates.sh dispatcher, per-template
// installers, systemd units, ssh material, CA, docker shims when declared,
// install.sh). The guest's install.sh extracts it to /opt/devm.
func (p *Provisioner) buildBundle() ([]byte, error) {
	in := devmbundle.BuildInput{
		Cfg:                 p.Cfg,
		RepoRoot:            p.WorkspaceVMPath,
		DaemonRuntimeDir:    p.DaemonRuntimeDir,
		CARootPEM:           p.CARootPEM,
		SSHAuthorizedPubkey: p.SSHAuthorizedPubkey,
		SSHHostPriv:         p.SSHHostPriv,
		SSHHostPub:          p.SSHHostPub,
		Pop:                 guestbin.Pop(),
	}
	if p.Cfg.Docker {
		in.DockerRuncShim = docker.Shim()
		in.DockerCLIShim = docker.DockerShim()
	}
	body, err := devmbundle.Build(in)
	if err != nil {
		return nil, fmt.Errorf("build devm bundle: %w", err)
	}
	return body, nil
}

// serviceUnits returns the sorted unit names of services with an actual
// in-VM process (Exec or Systemd). Routing-only declarations (proxy
// hostnames with no process) are skipped — there's no unit to start.
func (p *Provisioner) serviceUnits() []string {
	var names []string
	for name, svc := range p.Cfg.Services {
		if svc.Systemd == "" && len(svc.Exec) == 0 {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// hasTemplates reports whether any service declares templates, so the
// script runs the /opt/devm/scripts/install-templates.sh dispatcher.
func (p *Provisioner) hasTemplates() bool {
	for _, svc := range p.Cfg.Services {
		if len(svc.Templates) > 0 {
			return true
		}
	}
	return false
}

// maskMounts resolves every declared mask into a MaskMount the
// script can bind-mount over the workspace path. Sorted by mask
// path for a deterministic script. Storage lives at
// /var/devm/masks/<project>/<path>/ — no per-service dimension
// (masks became top-level in v0.9.18, decoupled from services).
func (p *Provisioner) maskMounts() []render.MaskMount {
	if len(p.Cfg.Masks) == 0 {
		return nil
	}
	paths := append([]string(nil), p.Cfg.Masks...)
	sort.Strings(paths)
	mounts := make([]render.MaskMount, 0, len(paths))
	for _, path := range paths {
		mounts = append(mounts, render.MaskMount{
			HostPath:    filepath.Join("/var/devm/masks", p.Cfg.Project.Name, path),
			MountTarget: filepath.Join(p.WorkspaceVMPath, path),
		})
	}
	return mounts
}

// provisionedMarker is the guest path whose presence indicates this VM has
// already completed first-boot provisioning. The composed script writes it
// on a successful first boot; markerExists reads it to set firstBoot.
const provisionedMarker = "/var/lib/devm/provisioned"

// markerExists reports whether the first-boot marker is present in the guest.
//
// ExecWithRetry, not Exec: this is the first guest call after boot, and a
// transient guest-agent transport drop here would misclassify a restart as a
// first boot — re-running the apt/install:/docker steps, where they fail and
// tear down a healthy VM. A genuine "marker absent" is a clean exit 1 (not a
// transport flake), so it is not retried.
func (p *Provisioner) markerExists(ctx context.Context) bool {
	return p.Tart.ExecWithRetry(ctx, p.VMName, []string{"test", "-f", provisionedMarker}).ExitCode == 0
}

// stageTracker records the most recent `::devm:stage:<name>::` marker seen
// on the streamed output, so a script failure can be classified by the
// stage it had reached. observe is called concurrently from the stdout and
// stderr scan goroutines, so it locks.
type stageTracker struct {
	mu    sync.Mutex
	stage string
}

func (s *stageTracker) observe(line string) {
	const prefix = "::devm:stage:"
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, prefix) || !strings.HasSuffix(t, "::") {
		return
	}
	name := strings.TrimSuffix(strings.TrimPrefix(t, prefix), "::")
	s.mu.Lock()
	s.stage = name
	s.mu.Unlock()
}

func (s *stageTracker) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stage
}
