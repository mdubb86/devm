// Package provision orchestrates per-project first-boot work in a
// freshly cloned Tart VM. The orchestrator (Task 9) calls
// Provisioner.Run after tart run + supervisor.Spawn succeed.
package provision

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
)

// tartExecer is the subset of *tart.Tart used by Provisioner. Defined as
// an interface so tests can inject fakes without shelling out to tart.
type tartExecer interface {
	Exec(ctx context.Context, name string, argv []string) tart.ExecResult
	ExecWithRetry(ctx context.Context, name string, argv []string) tart.ExecResult
}

// Provisioner runs the per-project first-boot sequence in a Tart VM.
type Provisioner struct {
	Tart   tartExecer
	VMName string
	Cfg    schema.Config

	// CARootPEM is the contents of ~/Library/Application Support/devm/ca/root.crt
	// (Ship 3 CA). The provisioner copies this into the VM at
	// /usr/local/share/ca-certificates/devm.crt and runs
	// update-ca-certificates so the VM trusts our CA for *.test HTTPS.
	CARootPEM []byte

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
	"install service units":   true,
	"systemctl daemon-reload": true,
	"enable + start services": true,
	"apply masks":             true,
}

// IsPostInstallFailure reports whether err is a StepFailure at or after
// the "install service units" step — i.e. a failure that leaves the VM in
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
	steps := []struct {
		name string
		fn   func(context.Context, io.Writer) error
	}{
		{"mkdir workspace parents", p.mkdirWorkspaceParents},
		{"install CA root", p.installCARoot},
		{"link with-devm-env into PATH", p.linkWithDevmEnv},
		{"write Caddyfile", p.writeCaddyfile},
		{"write dnsmasq config", p.writeDnsmasqConfig},
		{"reload base services", p.reloadBaseServices},
		{"apt-get update", p.aptUpdate},
		{"apt-get install packages", p.aptInstall},
		{"scaffold user firewall chain", p.scaffoldUserFirewallChain},
		{"run install commands", p.runInstallCommands},
		{"docker feature", p.dockerFeature},
		{"install templates", p.installTemplates},
		{"install service units", p.installServiceUnits},
		{"systemctl daemon-reload", p.daemonReload},
		{"apply egress enforcement", p.applyEgressEnforcement},
		{"enable + start services", p.enableStartServices},
		{"apply masks", p.applyMasks},
	}
	for _, step := range steps {
		fmt.Fprintf(w, "\n[step: %s]\n", step.name)
		if err := step.fn(ctx, w); err != nil {
			return &StepFailure{Step: step.name, Err: err}
		}
	}
	return nil
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

func (p *Provisioner) mkdirWorkspaceParents(ctx context.Context, w io.Writer) error {
	parent := filepath.Dir(p.WorkspaceVMPath)
	return p.exec(ctx, w, "sudo", "mkdir", "-p", parent)
}

// linkWithDevmEnv symlinks the workspace-share wrapper into /usr/local/bin
// so `with-devm-env` resolves from any shell in the VM without depending on
// .devm/.env having been sourced first. Idempotent via `ln -sf`.
func (p *Provisioner) linkWithDevmEnv(ctx context.Context, w io.Writer) error {
	src := filepath.Join(p.WorkspaceVMPath, ".devm", "scripts", "with-devm-env")
	return p.exec(ctx, w, "sudo", "ln", "-sf", src, "/usr/local/bin/with-devm-env")
}

func (p *Provisioner) installCARoot(ctx context.Context, w io.Writer) error {
	return p.execShell(ctx, w, p.installCARootScript())
}

// installCARootScript builds the shell script that installs the devm CA
// into the guest's CApath and verifies it lands in the merged bundle file.
//
// tart.Exec doesn't expose stdin streaming, so we base64-encode the PEM and
// decode inside the VM via a shell pipeline.
//
// --fresh is required: on a base image where update-ca-certificates has
// already run, its cached state can cause a subsequent run to skip the
// merge step, so /etc/ssl/certs/ca-certificates.crt never picks up
// devm.crt even though the CApath symlink is created. --fresh rebuilds
// from scratch, and the trailing grep makes provisioning fail loud if the
// bundle merge ever regresses.
func (p *Provisioner) installCARootScript() string {
	encoded := base64.StdEncoding.EncodeToString(p.CARootPEM)
	return fmt.Sprintf(
		`set -e
echo %s | base64 -d | sudo tee /usr/local/share/ca-certificates/devm.crt > /dev/null
sudo update-ca-certificates --fresh
grep -q "devm Local CA" /etc/ssl/certs/ca-certificates.crt || {
    echo "FAIL: devm CA installed to CApath but not merged into ca-certificates.crt bundle" >&2
    exit 1
}`,
		encoded,
	)
}

func (p *Provisioner) writeCaddyfile(ctx context.Context, w io.Writer) error {
	contents := render.Caddyfile(p.Cfg)
	encoded := base64.StdEncoding.EncodeToString([]byte(contents))
	script := fmt.Sprintf(
		`echo %s | base64 -d | sudo tee /etc/caddy/Caddyfile > /dev/null`,
		encoded,
	)
	return p.execShell(ctx, w, script)
}

func (p *Provisioner) writeDnsmasqConfig(ctx context.Context, w io.Writer) error {
	cfg := render.DnsmasqConfig()
	encoded := base64.StdEncoding.EncodeToString(cfg)
	script := fmt.Sprintf(
		`echo %s | base64 -d | sudo tee /etc/dnsmasq.d/devm-test.conf > /dev/null`,
		encoded,
	)
	return p.execShell(ctx, w, script)
}

func (p *Provisioner) applyEgressEnforcement(ctx context.Context, w io.Writer) error {
	if p.EnforceEgress == nil {
		fmt.Fprintln(w, "(no EnforceEgress callback set — skipping)")
		return nil
	}
	return p.EnforceEgress(ctx)
}

// scaffoldUserFirewallChain creates the `inet devm_filter/user_output`
// and `inet devm_filter/user_forward` chains so recipes can
// `nft add rule inet devm_filter user_output ...` (host-egress escape)
// or `... user_forward ...` (container-egress escape) during install:
// — the chains must exist before rules can be added to them.
// applyEgressEnforcement runs later and snapshots whatever ended up
// in each chain to /etc/nftables.d/user_output.conf and
// /etc/nftables.d/user_forward.conf so recipe-added rules survive
// VM reboot.
//
// FLUSHES both chains on every provision run: each cold-start
// re-executes install: from scratch, so any `nft add rule` there would
// otherwise pile up duplicate rules across successive re-provisions
// on the same disk (devm shell → change install: → devm shell). The
// flush makes install: commands the single source of truth for what
// ends up in the user chains — reproducible from devm.yaml.
//
// The `add table` / `add chain` are idempotent (no-op if exists). The
// chains have no type/hook, so a rule added to them has zero effect
// until applyEgressEnforcement wires the parent chains'
// `jump user_output` / `jump user_forward`.
func (p *Provisioner) scaffoldUserFirewallChain(ctx context.Context, w io.Writer) error {
	script := `sudo nft -f - <<'EOF'
add table inet devm_filter
add chain inet devm_filter user_output
add chain inet devm_filter user_forward
flush chain inet devm_filter user_output
flush chain inet devm_filter user_forward
EOF
`
	return p.execShell(ctx, w, script)
}

func (p *Provisioner) reloadBaseServices(ctx context.Context, w io.Writer) error {
	// reload-or-restart handles both "config changed, reload" and
	// "service not running yet, start it".
	//
	// Only caddy is reloaded here. Dnsmasq is not yet running: at this
	// point systemd-resolved still holds :53. The apply-egress-enforcement
	// step (fires post-provision) masks resolved and starts dnsmasq — so
	// dnsmasq's config in /etc/dnsmasq.d/devm-test.conf (written at
	// writeDnsmasqConfig above) becomes active there.
	return p.exec(ctx, w, "sudo", "systemctl", "reload-or-restart", "caddy")
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
	wrapper := filepath.Join(p.WorkspaceVMPath, ".devm", "scripts", "with-devm-env")
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
// No-op otherwise.
func (p *Provisioner) dockerFeature(ctx context.Context, w io.Writer) error {
	if !p.Cfg.Docker {
		fmt.Fprintln(w, "(docker: false — skipping)")
		return nil
	}
	return docker.Install(ctx, w, p)
}

// installTemplates runs the install-templates.sh dispatcher inside the VM,
// which loops over .devm/templates/*.sh and executes each per-template
// installer. Each installer is idempotent (atomic rename over the target
// path) so re-running on warm restart is safe.
//
// No-op when no templates are declared (empty .devm/templates/ dir causes
// the dispatcher to exit 0 immediately).
//
// Runs THROUGH with-devm-env so $WORKSPACE is set — the dispatcher uses
// it to locate .devm/templates.
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
	wrapper := filepath.Join(p.WorkspaceVMPath, ".devm", "scripts", "with-devm-env")
	dispatcher := filepath.Join(p.WorkspaceVMPath, ".devm", "scripts", "install-templates.sh")
	return p.exec(ctx, w, wrapper, "bash", dispatcher)
}

func (p *Provisioner) installServiceUnits(ctx context.Context, w io.Writer) error {
	if len(p.Cfg.Services) == 0 {
		fmt.Fprintln(w, "(no services declared)")
		return nil
	}
	for name, svc := range p.Cfg.Services {
		// Merge top-level env into per-service env so cfg.Env entries
		// (including !secret refs) reach the rendered systemd unit.
		// Per-service env wins on key collision — explicit beats
		// inherited.
		merged := make(map[string]schema.EnvValue, len(p.Cfg.Env)+len(svc.Env))
		for k, v := range p.Cfg.Env {
			merged[k] = v
		}
		for k, v := range svc.Env {
			merged[k] = v
		}
		svc.Env = merged
		unit := render.RenderService(name, svc)
		encoded := base64.StdEncoding.EncodeToString(unit)
		unitPath := fmt.Sprintf("/etc/systemd/system/%s.service", name)
		script := fmt.Sprintf(
			`echo %s | base64 -d | sudo tee %s > /dev/null`,
			encoded, unitPath,
		)
		if err := p.execShell(ctx, w, script); err != nil {
			return err
		}
	}
	return nil
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
				p.Cfg.Project.ID, svcName, m.Path)
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
