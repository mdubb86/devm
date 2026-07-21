package render

import (
	"fmt"
	"strings"

	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/schema"
)

// Guest-side paths the composed provisioning script references. These
// mirror the devmbundle layout (GuestRoot/scripts/...), duplicated as
// literals here because devmbundle imports render — importing it back
// would be a cycle.
const (
	guestWrapper     = "/opt/devm/scripts/with-devm-env"
	guestDispatcher  = "/opt/devm/scripts/install-templates.sh"
	guestStartupSh   = "/opt/devm/startup.sh"
	provisionedMark  = "/var/lib/devm/provisioned"
	inProgressMarker = "/run/devm/provisioning"
)

// MaskMount is one resolved service mask overlay: a per-service host dir
// bind-mounted over a workspace path, owned by the service's run-user.
// The daemon resolves the paths + owner (it holds the workspace path and
// project name); the script only emits the mkdir/chown/mount.
type MaskMount struct {
	HostPath    string // /var/devm/masks/<project>/<service>/<path>
	MountTarget string // <workspace>/<path>
	Owner       string // service run-user (default devm)
}

// ProvisionScriptInput is everything the composed guest provisioning script
// needs baked in. The daemon builds this from schema.Config, then
// RenderProvisionOpenScript/RenderProvisionEnforcedScript turn it into the
// two bash scripts run around the softnet ENFORCED flip.
type ProvisionScriptInput struct {
	FirstBoot        bool
	Packages         []string // apt packages (first boot only)
	Install          []string // install: commands (first boot only)
	Docker           bool     // run the docker feature (first boot only)
	InstallTemplates bool     // run the template dispatcher (every open boot)
	Startup          []string // startup: commands (every boot)
	// Scripts is the resolved scripts library from Config.Scripts. Used
	// to expand ">NAME" references in Install and Startup entries at
	// render time. The map is passed as-is by the caller; validation
	// (name shape, empty commands, undefined refs) has already happened
	// in schema.Config.Validate — the renderer trusts what it receives.
	Scripts  map[string][]string
	Services []string // service unit names to enable+start (health-polled)
	Masks    []MaskMount

	// StepTimeoutSeconds bounds every install:/startup: command (the
	// `timeout %d` wrapping both stages). Zero means "unset" and falls back
	// to defaultStepTimeoutSeconds — the daemon fills this from
	// DEVM_INSTALL_STEP_TIMEOUT_S (see provision.Provisioner), so it is only
	// ever zero in tests that don't care about the override.
	StepTimeoutSeconds int
}

// hasOpenWork reports whether the open egress window is needed this boot.
func (in ProvisionScriptInput) hasOpenWork() bool {
	return in.FirstBoot || len(in.Startup) > 0 || in.InstallTemplates
}

// defaultStepTimeoutSeconds is the fallback for ProvisionScriptInput.
// StepTimeoutSeconds when unset. The composed script is streamed over a
// single `tart exec`, so a command that hangs blocks the whole exec — not
// just its own step — which would hang `devm shell` indefinitely. Matches
// the old per-step provisioner's DEVM_INSTALL_STEP_TIMEOUT_S default.
const defaultStepTimeoutSeconds = 600

// serviceHealthPollSeconds bounds how long the composed script waits for
// each declared service to reach a healthy state before aborting. Matches
// the old per-step provisioner's enableStartServices poll budget.
const serviceHealthPollSeconds = 10

// RenderProvisionOpenScript composes the OPEN-egress half of provisioning:
// header, the in-progress marker WRITE, bundle extraction, the unconditional
// guest-nft flush, and the entire open-egress work window (packages,
// install:, docker, templates, startup:). It is delivered as `bash -c
// '<this>'` with the bundle tar on stdin, run while softnet is OPEN — before
// the ENFORCED flip. Stages are marked with `::devm:stage:<name>::` on
// stdout; the daemon parses them to drive the spinner AND to classify a
// failure by the stage it reached. `set -eo pipefail` makes any failing
// command abort the script immediately.
//
// The in-progress marker it writes is cleaned up by
// RenderProvisionEnforcedScript, not here: a crash between the two scripts
// (host sleep, daemon restart, killed exec) must leave the marker set, so
// the next `devm shell` sees a dirty VM and tears it down rather than
// resuming onto an unknown intermediate state — this is what closes the
// crash-hole where services could otherwise come up under open egress.
func RenderProvisionOpenScript(in ProvisionScriptInput) []byte {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	stepTimeout := in.StepTimeoutSeconds
	if stepTimeout <= 0 {
		stepTimeout = defaultStepTimeoutSeconds
	}

	p("#!/bin/bash")
	p("set -eo pipefail")
	// (1) in-progress marker FIRST. /run is tmpfs and /run/devm doesn't
	// exist yet; the script runs as the unprivileged devm user, so both
	// the directory and the marker need sudo.
	p("sudo mkdir -p /run/devm")
	p("sudo touch %s", inProgressMarker)
	// (2) extract the bundle (tar on stdin) and run the extractor for CA/symlinks
	p("sudo mkdir -p /opt/devm")
	p("sudo tar -xC /opt/devm")    // consumes stdin
	p("sudo /opt/devm/install.sh") // existing CA install + PATH symlink

	// The base image bakes a policy-drop nftables lock (image/builder.go's
	// nftables-locked.conf) into every fresh clone. softnet is the egress
	// boundary now, not this guest ruleset, and a leftover policy-drop
	// would drop softnet's own egress too — flushed unconditionally, every
	// boot, regardless of whether this boot has an open work window.
	p("sudo nft flush ruleset")

	if in.hasOpenWork() {
		p("echo ::devm:stage:open::")
		if in.FirstBoot {
			if len(in.Packages) > 0 {
				p("echo ::devm:stage:packages::")
				p("sudo apt-get update -y")
				quoted := make([]string, len(in.Packages))
				for i, pkg := range in.Packages {
					quoted[i] = shellSingleQuoted(pkg)
				}
				p("sudo apt-get install -y %s", strings.Join(quoted, " "))
			}
			if len(in.Install) > 0 {
				p("echo ::devm:stage:install::")
				for i, cmd := range in.Install {
					p("echo ::devm:progress:install:%d:%d::", i+1, len(in.Install))
					body := cmd
					if name, ok := schema.ParseScriptRef(cmd); ok {
						body = strings.Join(in.Scripts[name], " && ")
					}
					p("timeout %d %s bash -eo pipefail -c %s", stepTimeout, guestWrapper, shellSingleQuoted(body))
				}
			}
			if in.Docker {
				p("echo ::devm:stage:docker::")
				// The docker install script (engine + runc-shim registration
				// via daemon.json + socket drop-in). The shim binaries are
				// bundle artifacts already installed by /opt/devm/install.sh.
				p("%s", docker.InstallScript())
				// dockerd joins the gate: disabled at boot, wanted by devm.target
				p("sudo systemctl disable docker.service")
				p("sudo mkdir -p /etc/systemd/system/devm.target.wants")
				p("sudo ln -sf /lib/systemd/system/docker.service /etc/systemd/system/devm.target.wants/docker.service")
			}
		}
		if in.InstallTemplates {
			p("echo ::devm:stage:templates::")
			// Runs THROUGH with-devm-env for auto-cd + terminfo setup. The
			// dispatcher loops over /opt/devm/templates/*.sh (each idempotent),
			// so re-running on a warm boot is safe.
			p("%s bash %s", guestWrapper, guestDispatcher)
		}
		if len(in.Startup) > 0 {
			p("echo ::devm:stage:startup::")
			// One timeout budget for the whole script, not per line inside
			// it: startup.sh's commands share a single bash process (env
			// exports / cd from an earlier line are visible to a later
			// one), and wrapping each line in its own `bash -c` subshell
			// would silently break that. install:'s commands, in
			// contrast, were always independent invocations (no shared
			// shell state), so each gets its own budget above.
			p("timeout %d %s bash %s", stepTimeout, guestWrapper, guestStartupSh)
		}
	}

	return []byte(b.String())
}

// RenderProvisionEnforcedScript composes the ENFORCED-egress half of
// provisioning, run immediately after RenderProvisionOpenScript succeeds
// and softnet has been flipped to ENFORCED. It is delivered as `bash -c
// '<this>'` with NO stdin — /opt/devm was already extracted by the open
// script. It starts and health-polls user services, then activates
// devm.target, and finally clears the in-progress marker the open script
// wrote — the LAST line, so the marker's presence remains a genuine
// "provisioning not yet fully complete" signal for the whole exec, not
// just this half.
func RenderProvisionEnforcedScript(in ProvisionScriptInput) []byte {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	p("#!/bin/bash")
	p("set -eo pipefail")

	// (3) enforce: stage boundary marking the classifier's teardown/
	// debuggable-VM split (stagesAfterInstall in provision.Provisioner) —
	// a failure at or before this point is the daemon's own enforcement
	// being broken, not the user's service. timesyncd's NTP config used
	// to be applied here at runtime; it's now baked into the base image
	// (image/provision-base.sh), so this stage has no work of its own
	// left, only masks below.
	p("echo ::devm:stage:enforce::")

	// (4) services phase: masks (bind-mount overlays the services write into),
	// then start + health-poll services BEFORE granting access. A failure from
	// here on leaves a provisioned VM whose user-declared service is what's
	// broken, so the classifier keeps the VM for in-place debugging.
	p("echo ::devm:stage:services::")
	for _, m := range in.Masks {
		p("sudo mkdir -p %s", shellSingleQuoted(m.HostPath))
		p("sudo chown %s %s", m.Owner, shellSingleQuoted(m.HostPath))
		p("sudo mkdir -p %s", shellSingleQuoted(m.MountTarget))
		p("sudo mount --bind %s %s", shellSingleQuoted(m.HostPath), shellSingleQuoted(m.MountTarget))
	}
	// Services are WantedBy=devm.target (disabled at boot), but we start them
	// EXPLICITLY here and poll — so a broken service aborts (exit 1) BEFORE
	// ssh/the target come up. Preserves "broken service is loud, no access".
	p("sudo systemctl daemon-reload")
	p("sudo systemctl unmask ssh")
	for _, svc := range in.Services {
		p("sudo systemctl enable %s.service", svc)
		p("sudo systemctl start %s.service", svc)
	}
	// Bounded health poll, not a single is-failed snapshot: a Type=simple
	// service that dies a beat after `start` returns would slip past a
	// one-shot check and let devm.target activate with a dead service.
	// One-shot-aware: a Type=oneshot unit without RemainAfterExit settles
	// at ActiveState=inactive/Result=success once its ExecStart exits 0 —
	// that's a pass, not a hang, so it's checked alongside is-active.
	for _, svc := range in.Services {
		p("svc_deadline=$((SECONDS+%d))", serviceHealthPollSeconds)
		p("while :; do")
		p("  if systemctl is-failed --quiet %s.service; then echo 'service %s failed' >&2; exit 1; fi", svc, svc)
		p("  systemctl is-active --quiet %s.service && break", svc)
		p("  [ \"$(systemctl show -p Result --value %s.service)\" = success ] && "+
			"[ \"$(systemctl show -p ActiveState --value %s.service)\" = inactive ] && break", svc, svc)
		p("  if [ \"$SECONDS\" -ge \"$svc_deadline\" ]; then echo 'service %s did not become healthy within %ds' >&2; exit 1; fi", svc, serviceHealthPollSeconds)
		p("  sleep 0.5")
		p("done")
	}

	// (5) first-boot completion marker — its presence flips the next boot off
	// the first-boot path. Written only after every step above succeeded
	// (set -eo pipefail), so it is a genuine "provisioning completed" signal.
	if in.FirstBoot {
		p("sudo mkdir -p /var/lib/devm")
		p("sudo touch %s", provisionedMark)
	}

	// (6) LAST functional line: activate the target — ssh + caddy + dnsmasq +
	// dockerd come up (services already healthy), enforced. Access is granted
	// ONLY after services are confirmed up.
	p("sudo systemctl start devm.target")
	// (7) cleanup marker on success — clears the marker RenderProvisionOpenScript
	// wrote, closing out the two-exec provisioning run.
	p("sudo rm -f %s", inProgressMarker)

	return []byte(b.String())
}
