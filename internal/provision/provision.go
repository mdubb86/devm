// Package provision orchestrates per-project first-boot work in a
// freshly cloned Tart VM. The orchestrator (Task 9) calls
// Provisioner.Run after tart run + supervisor.Spawn succeed.
package provision

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/sandbox/tart"
	"github.com/mdubb86/devm/internal/schema"
)

// Provisioner runs the per-project first-boot sequence in a Tart VM.
type Provisioner struct {
	Tart   *tart.Tart
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
	return p.exec(ctx, w, "bash", "-c", script)
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
	return p.exec(ctx, w, "sudo", "systemctl", "reload-or-restart", "devm-caddy")
}

func (p *Provisioner) aptUpdate(ctx context.Context, w io.Writer) error {
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

func (p *Provisioner) runInstallCommands(ctx context.Context, w io.Writer) error {
	if len(p.Cfg.Install) == 0 {
		fmt.Fprintln(w, "(no install commands)")
		return nil
	}
	for i, command := range p.Cfg.Install {
		fmt.Fprintf(w, "[%d/%d] %s\n", i+1, len(p.Cfg.Install), command)
		if err := p.execShell(ctx, w, command); err != nil {
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

func (p *Provisioner) enableStartServices(ctx context.Context, w io.Writer) error {
	for name, svc := range p.Cfg.Services {
		// Skip routing-only service blocks (no Exec, no Systemd) — they
		// declare a hostname+port for the proxy but have no in-VM process.
		if svc.Systemd == "" && len(svc.Exec) == 0 {
			fmt.Fprintf(w, "(skip %s — routing-only declaration)\n", name)
			continue
		}
		unitName := name + ".service"
		if err := p.exec(ctx, w, "sudo", "systemctl", "enable", "--now", unitName); err != nil {
			return err
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
