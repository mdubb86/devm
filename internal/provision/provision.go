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

	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
)

// tartExecer is the subset of *tart.Tart used by Provisioner. Defined as
// an interface so tests can inject fakes without shelling out to tart.
type tartExecer interface {
	Exec(ctx context.Context, name string, argv []string) tart.ExecResult
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
}

// Run executes the full provisioning sequence. Streams progress and
// per-step output to w. Returns on the first failure.
//
// Each step's output is prefixed with [step: <name>] so failures
// point clearly.
func (p *Provisioner) Run(ctx context.Context, w io.Writer) error {
	steps := []struct {
		name string
		fn   func(context.Context, io.Writer) error
	}{
		{"mkdir workspace parents", p.mkdirWorkspaceParents},
		{"install CA root", p.installCARoot},
		{"write Caddyfile", p.writeCaddyfile},
		{"write dnsmasq config", p.writeDnsmasqConfig},
		{"reload base services", p.reloadBaseServices},
		{"apt-get update", p.aptUpdate},
		{"apt-get install packages", p.aptInstall},
		{"run install commands", p.runInstallCommands},
		{"install service units", p.installServiceUnits},
		{"systemctl daemon-reload", p.daemonReload},
		{"enable + start services", p.enableStartServices},
		{"apply masks", p.applyMasks},
	}
	for _, step := range steps {
		fmt.Fprintf(w, "\n[step: %s]\n", step.name)
		if err := step.fn(ctx, w); err != nil {
			return fmt.Errorf("provision step %q: %w", step.name, err)
		}
	}
	return nil
}

// exec runs the given argv via tart.Exec, writes captured stdout +
// stderr to w, and returns an error if exit code is nonzero.
func (p *Provisioner) exec(ctx context.Context, w io.Writer, argv ...string) error {
	r := p.Tart.Exec(ctx, p.VMName, argv)
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

func (p *Provisioner) mkdirWorkspaceParents(ctx context.Context, w io.Writer) error {
	parent := filepath.Dir(p.WorkspaceVMPath)
	return p.exec(ctx, w, "sudo", "mkdir", "-p", parent)
}

func (p *Provisioner) installCARoot(ctx context.Context, w io.Writer) error {
	// tart.Exec doesn't expose stdin streaming, so we base64-encode the
	// PEM and decode inside the VM via a shell pipeline.
	encoded := base64.StdEncoding.EncodeToString(p.CARootPEM)
	script := fmt.Sprintf(
		`echo %s | base64 -d | sudo tee /usr/local/share/ca-certificates/devm.crt > /dev/null && sudo update-ca-certificates`,
		encoded,
	)
	return p.execShell(ctx, w, script)
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

func (p *Provisioner) reloadBaseServices(ctx context.Context, w io.Writer) error {
	// reload-or-restart handles both "config changed, reload" and
	// "service not running yet, start it".
	if err := p.exec(ctx, w, "sudo", "systemctl", "reload-or-restart", "dnsmasq"); err != nil {
		return err
	}
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
	for i, command := range p.Cfg.Install {
		fmt.Fprintf(w, "[%d/%d] %s\n", i+1, len(p.Cfg.Install), command)
		stepCtx, cancel := context.WithTimeout(ctx, budget)
		err := p.execShell(stepCtx, w, command)
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
	// Collect non-routing-only service names, sorted for determinism.
	var names []string
	for name, svc := range p.Cfg.Services {
		// Skip routing-only service blocks (no Exec, no Systemd) — they
		// declare a hostname+port for the proxy but have no in-VM process.
		if svc.Systemd == "" && len(svc.Exec) == 0 {
			fmt.Fprintf(w, "(skip %s — routing-only declaration)\n", name)
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		unitName := name + ".service"
		if err := p.exec(ctx, w, "sudo", "systemctl", "enable", "--now", unitName); err != nil {
			return err
		}
	}

	// Poll each service's active state. A service in "failed" state surfaces
	// immediately as a structured error; all others wait up to healthTotalBudget.
	deadline := time.Now().Add(healthTotalBudget)
	for _, name := range names {
		for {
			r := p.Tart.Exec(ctx, p.VMName, []string{"systemctl", "is-active", name})
			state := strings.TrimSpace(r.Stdout)
			if r.ExitCode == 0 {
				break
			}
			if state == "failed" {
				return fmt.Errorf("service %q did not become active: status=%s", name, state)
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("service %q did not become active: status=%s (timeout)", name, state)
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
		for _, m := range svc.Masks {
			maskHostPath := filepath.Join("/var/devm/masks",
				p.Cfg.Project.ID, svcName, m.Path)
			mountTarget := filepath.Join(p.WorkspaceVMPath, m.Path)
			script := strings.Join([]string{
				"sudo", "mkdir", "-p", maskHostPath, "&&",
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
