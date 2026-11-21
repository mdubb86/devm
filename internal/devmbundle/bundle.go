package devmbundle

import (
	"archive/tar"
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/scripts"
)

// zeroTime is a deterministic mtime for every tar header. Fixed value
// so two builds of the same cfg produce byte-identical archives.
var zeroTime = time.Unix(0, 0).UTC()

// BuildInput carries the inputs devmbundle.Build needs. Fields grow
// as more artifacts fold into the bundle; existing fields are stable.
type BuildInput struct {
	Cfg      schema.Config
	RepoRoot string

	// DaemonRuntimeDir is the daemon's runtime directory (identity.Config.
	// RuntimeDir()), threaded through to render.RenderTemplates as the
	// root of its scratch namespace for the in-memory template-installer
	// map.
	DaemonRuntimeDir string

	// TLD is the daemon's identity TLD (identity.Config.TLD). Threaded
	// into gdevm-serve.service via RenderGdevmServeUnit so the in-guest
	// handler can match preview.<project>.<tld>.
	TLD string

	CARootPEM []byte

	SSHAuthorizedPubkey []byte
	SSHHostPriv         []byte
	SSHHostPub          []byte

	// SSHClientConfig is the body of /home/devm/.ssh/config. Nil = ssh:
	// list empty/absent for this project — install.sh then removes the
	// file and disables the agent-bridge unit.
	SSHClientConfig []byte

	// SSHJumpClientPriv is the PEM-encoded per-project ed25519 private
	// key the guest uses to authenticate to this project's devm-jump.
	// Nil when SSHClientConfig is nil.
	SSHJumpClientPriv []byte

	// SSHKnownHostsAppend is lines appended to /home/devm/.ssh/known_hosts
	// (never overwritten). First line is the devm-jump server's host key;
	// subsequent lines are each resolved target's known_hosts entry
	// pulled from the Mac's ~/.ssh/known_hosts. Nil when SSHClientConfig
	// is nil.
	SSHKnownHostsAppend []byte

	// SSHAgentBridgeUnit is the systemd unit body for gdevm
	// ssh-agent-bridge. Nil when SSHClientConfig is nil.
	SSHAgentBridgeUnit []byte

	DockerRuncShim []byte
	DockerCLIShim  []byte

	// Gdevm is the compiled linux/arm64 gdevm binary (see cmd/gdevm) —
	// the single guest-side dispatcher carrying pop / propose / run
	// subcommands. Written to bin/gdevm in the tar; install.sh's
	// existing bin/* loop symlinks it to /usr/local/bin/gdevm. Nil in
	// tests that don't need it.
	Gdevm []byte

	// CommandsManifest is the pre-rendered body of /opt/devm/commands.json
	// (see internal/render/commands.go's RenderCommandsManifest). Empty ⇒
	// no file emitted.
	CommandsManifest []byte

	// MutagenAgentLinuxArm64 is the linux/arm64 mutagen-agent binary
	// bytes. Pre-installed in the guest so mutagen's transport skips its
	// SCP bootstrap. Empty ⇒ no file emitted (test-only; production
	// always populates via mutagen.LinuxArm64Agent()).
	MutagenAgentLinuxArm64 []byte

	// MutagenVersion is the pinned mutagen version (e.g. "0.18.1", no
	// "v" prefix). Used to render install.sh's mutagen-agent install
	// path. Empty ⇒ Build fails loud — this field must always be set in
	// real bundles.
	MutagenVersion string

	// SizingConf is the body of /etc/devm/sizing.conf — the two-line
	// swap=/build_cache= directive file consumed by install.sh's sizing
	// setup. Populated unconditionally by build_input_for.go via
	// render.RenderSizingConf; Build fails loud when empty.
	SizingConf []byte

	// DevmSh is the user's devm.sh file content (hook definitions for
	// install/startup/snapshot entries). Emitted to /opt/devm/devm.sh
	// when non-empty — the snapshot hook runner sources it before
	// invoking backup_<name> / restore_<name>. Empty ⇒ no file emitted.
	DevmSh []byte

	// DevmMeSh is the user's devm.me.sh file content (per-user hook
	// overrides). Emitted to /opt/devm/devm.me.sh when non-empty —
	// the snapshot hook runner sources it if present, for symmetry
	// with Config.LoadFunctions. Empty ⇒ no file emitted.
	DevmMeSh []byte
}

// Build returns a tar archive containing the devm-owned artifacts the
// guest needs at /opt/devm/. The daemon pipes this into the guest via
// PipeIntoShell + GuestInstallScript.
func Build(in BuildInput) ([]byte, error) {
	if len(in.SizingConf) == 0 {
		return nil, fmt.Errorf("sizing conf missing")
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	etcEnvBody, err := render.RenderEtcEnvironment(in.Cfg)
	if err != nil {
		return nil, fmt.Errorf("render /etc/environment: %w", err)
	}
	if err := writeEntry(tw, "etc/environment", 0o644, []byte(etcEnvBody)); err != nil {
		return nil, err
	}

	if len(in.DevmSh) > 0 {
		if err := writeEntry(tw, "devm.sh", 0o644, in.DevmSh); err != nil {
			return nil, fmt.Errorf("write devm.sh: %w", err)
		}
	}
	if len(in.DevmMeSh) > 0 {
		if err := writeEntry(tw, "devm.me.sh", 0o644, in.DevmMeSh); err != nil {
			return nil, fmt.Errorf("write devm.me.sh: %w", err)
		}
	}

	if err := writeEntry(tw, "scripts/with-devm-env", 0o755, []byte(scripts.WithDevmEnv)); err != nil {
		return nil, err
	}
	if err := writeEntry(tw, "scripts/install-templates.sh", 0o755, []byte(scripts.InstallTemplates)); err != nil {
		return nil, err
	}
	if err := writeEntry(tw, "profile.d/devm.sh", 0o644, []byte(scripts.EtcProfileDevm)); err != nil {
		return nil, err
	}

	templates, err := render.RenderTemplates(in.Cfg, in.RepoRoot, in.DaemonRuntimeDir, in.Cfg.Project.Name)
	if err != nil {
		return nil, fmt.Errorf("render templates: %w", err)
	}
	// Deterministic ordering: sort keys before writing.
	names := sortedKeys(templates)
	for _, name := range names {
		if err := writeEntry(tw, "templates/"+filepath.Base(name), 0o755, []byte(templates[name])); err != nil {
			return nil, err
		}
	}

	installBody, err := render.RenderInstallScript(in.MutagenVersion)
	if err != nil {
		return nil, fmt.Errorf("render install.sh: %w", err)
	}
	if err := writeEntry(tw, "install.sh", 0o755, installBody); err != nil {
		return nil, err
	}

	if len(in.CARootPEM) > 0 {
		if err := writeEntry(tw, "ca/devm.crt", 0o644, in.CARootPEM); err != nil {
			return nil, err
		}
	}

	if err := writeEntry(tw, "dnsmasq/devm-test.conf", 0o644, render.DnsmasqConfig()); err != nil {
		return nil, err
	}

	// gdevm-serve: devm's own guest-side state daemon. Unlike the
	// user-declared units below, this one ships in every bundle
	// unconditionally — it isn't gated on Cfg.Services, so a
	// repo-less project still gets it. install.sh installs the unit
	// file and (re)starts the service on every run, cold-start or
	// bundle refresh alike.
	if err := writeEntry(tw, "systemd/gdevm-serve.service", 0o644, render.RenderGdevmServeUnit(in.TLD)); err != nil {
		return nil, fmt.Errorf("gdevm-serve unit entry: %w", err)
	}

	// Snapshot timers: one `.service` + `.timer` pair per snapshots
	// entry that carries a cron: schedule. install.sh enables each
	// timer and disables/removes any timer whose name has since been
	// dropped from the declared set, using the manifest written here.
	var snapshotManifest []string
	for name, snap := range in.Cfg.Snapshots.Iter() {
		if snap.Cron == nil {
			continue
		}
		svc := render.RenderSnapshotService(name)
		if err := writeEntry(tw, "systemd/"+schema.SnapshotUnitPrefix+name+".service", 0o644, svc); err != nil {
			return nil, fmt.Errorf("snapshot service unit %s: %w", name, err)
		}
		tmr, err := render.RenderSnapshotTimer(name, *snap.Cron)
		if err != nil {
			return nil, fmt.Errorf("snapshot timer unit %s: %w", name, err)
		}
		if err := writeEntry(tw, "systemd/"+schema.SnapshotUnitPrefix+name+".timer", 0o644, tmr); err != nil {
			return nil, fmt.Errorf("snapshot timer unit %s: %w", name, err)
		}
		snapshotManifest = append(snapshotManifest, name)
	}
	// Always write the manifest, even when empty: tar extraction is
	// additive over the existing /opt/devm tree, so a prior bundle's
	// non-empty manifest would survive otherwise and install.sh's
	// orphan cleanup would never see the drop.
	var manifestBody string
	if len(snapshotManifest) > 0 {
		manifestBody = strings.Join(snapshotManifest, "\n") + "\n"
	}
	if err := writeEntry(tw, "snapshots/manifest", 0o644, []byte(manifestBody)); err != nil {
		return nil, fmt.Errorf("snapshots manifest: %w", err)
	}

	// Cron jobs: a service, timer, and wrapper per cron entry. Timers are
	// never enabled: the enforced provisioning step starts them at boot,
	// and install.sh restarts them once devm.target is active. install.sh
	// removes any entry whose name has since been dropped, using the
	// manifest written here.
	cronFns := make([]string, 0, len(in.Cfg.Cron))
	for fn := range in.Cfg.Cron {
		cronFns = append(cronFns, fn)
	}
	sort.Strings(cronFns)
	var cronEnv map[string]string
	if len(cronFns) > 0 {
		cronEnv = make(map[string]string, len(in.Cfg.Env))
		for k, v := range in.Cfg.Env {
			cronEnv[k] = v.Render()
		}
	}
	for _, fn := range cronFns {
		svc, tmr, wrapper, err := render.RenderCronJob(fn, in.Cfg.Cron[fn], cronEnv, in.Cfg.Path)
		if err != nil {
			return nil, err
		}
		if err := writeEntry(tw, "systemd/"+render.CronServiceUnit(fn), 0o644, svc); err != nil {
			return nil, fmt.Errorf("cron service unit %s: %w", fn, err)
		}
		if err := writeEntry(tw, "systemd/"+render.CronTimerUnit(fn), 0o644, tmr); err != nil {
			return nil, fmt.Errorf("cron timer unit %s: %w", fn, err)
		}
		if err := writeEntry(tw, strings.TrimPrefix(render.CronWrapperPath(fn), GuestRoot+"/"), 0o755, wrapper); err != nil {
			return nil, fmt.Errorf("cron wrapper %s: %w", fn, err)
		}
	}
	// Always written, even when empty, for the same reason as the
	// snapshots manifest.
	var cronManifest string
	if len(cronFns) > 0 {
		cronManifest = strings.Join(cronFns, "\n") + "\n"
	}
	if err := writeEntry(tw, "cron/manifest", 0o644, []byte(cronManifest)); err != nil {
		return nil, fmt.Errorf("cron manifest: %w", err)
	}

	// One systemd unit per service that will actually run in-guest.
	// Routing-only services (no Exec, no Systemd) contribute proxy config
	// only and don't get a unit; matches enableStartServices' skip logic.
	svcNames := make([]string, 0, in.Cfg.Services.Len())
	for name := range in.Cfg.Services.Entries {
		svcNames = append(svcNames, name)
	}
	sort.Strings(svcNames)
	for _, name := range svcNames {
		svc := in.Cfg.Services.Entries[name]
		if svc.Systemd == "" && svc.ExecFunc == "" && len(svc.ExecArgv) == 0 {
			continue
		}
		// Merge top-level env into per-service env so cfg.Env entries
		// (including !secret refs) reach the rendered systemd unit.
		// Per-service env wins on key collision — explicit beats inherited.
		merged := make(map[string]schema.EnvValue, len(in.Cfg.Env)+len(svc.Env))
		for k, v := range in.Cfg.Env {
			merged[k] = v
		}
		for k, v := range svc.Env {
			merged[k] = v
		}
		svc.Env = merged
		unit, wrapperPath, wrapperBody, err := render.RenderService(svc, name, nil, in.Cfg.Path)
		if err != nil {
			return nil, fmt.Errorf("render service %q unit: %w", name, err)
		}
		if err := writeEntry(tw, "systemd/"+name+".service", 0o644, unit); err != nil {
			return nil, err
		}
		if wrapperPath != "" {
			rel := strings.TrimPrefix(wrapperPath, GuestRoot+"/")
			if err := writeEntry(tw, rel, 0o755, wrapperBody); err != nil {
				return nil, err
			}
		}
	}

	if len(in.SSHAuthorizedPubkey) > 0 {
		if err := writeEntry(tw, "ssh/authorized_keys", 0o600, in.SSHAuthorizedPubkey); err != nil {
			return nil, err
		}
	}
	if len(in.SSHHostPriv) > 0 {
		if err := writeEntry(tw, "ssh/ssh_host_ed25519_key", 0o600, in.SSHHostPriv); err != nil {
			return nil, err
		}
	}
	if len(in.SSHHostPub) > 0 {
		if err := writeEntry(tw, "ssh/ssh_host_ed25519_key.pub", 0o644, in.SSHHostPub); err != nil {
			return nil, err
		}
	}

	if err := writeEntry(tw, "sizing/conf", 0o644, in.SizingConf); err != nil {
		return nil, fmt.Errorf("write sizing/conf: %w", err)
	}

	if in.SSHClientConfig != nil {
		if err := writeEntry(tw, "ssh-client/config", 0o600, in.SSHClientConfig); err != nil {
			return nil, fmt.Errorf("write ssh-client/config: %w", err)
		}
		if err := writeEntry(tw, "ssh-client/devm-jump-id", 0o600, in.SSHJumpClientPriv); err != nil {
			return nil, fmt.Errorf("write ssh-client/devm-jump-id: %w", err)
		}
		if err := writeEntry(tw, "ssh-client/known_hosts", 0o600, in.SSHKnownHostsAppend); err != nil {
			return nil, fmt.Errorf("write ssh-client/known_hosts: %w", err)
		}
		if err := writeEntry(tw, "ssh-client/devm-ssh-agent-bridge.service", 0o644, in.SSHAgentBridgeUnit); err != nil {
			return nil, fmt.Errorf("write ssh-client/devm-ssh-agent-bridge.service: %w", err)
		}
	}

	if in.Cfg.DockerEnabled() {
		if len(in.DockerRuncShim) == 0 || len(in.DockerCLIShim) == 0 {
			return nil, fmt.Errorf("docker.enabled=true requires DockerRuncShim and DockerCLIShim bytes")
		}
		if err := writeEntry(tw, "bin/devm-runc-shim", 0o755, in.DockerRuncShim); err != nil {
			return nil, err
		}
		if err := writeEntry(tw, "bin/docker", 0o755, in.DockerCLIShim); err != nil {
			return nil, err
		}
	}

	if len(in.Gdevm) > 0 {
		if err := writeEntry(tw, "bin/gdevm", 0o755, in.Gdevm); err != nil {
			return nil, fmt.Errorf("gdevm entry: %w", err)
		}
	}

	if len(in.CommandsManifest) > 0 {
		if err := writeEntry(tw, "commands.json", 0o644, in.CommandsManifest); err != nil {
			return nil, err
		}
	}
	if len(in.MutagenAgentLinuxArm64) > 0 {
		if err := writeEntry(tw, "bin/mutagen-agent", 0o755, in.MutagenAgentLinuxArm64); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeEntry(tw *tar.Writer, name string, mode int64, body []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(body)),
		ModTime: zeroTime,
		Uid:     0,
		Gid:     0,
		Format:  tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("write tar body %s: %w", name, err)
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Simple insertion sort; templates are small.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
