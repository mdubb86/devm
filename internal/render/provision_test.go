package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderProvisionOpenScript_Structure(t *testing.T) {
	in := ProvisionScriptInput{
		FirstBoot:        true,
		Packages:         []string{"jq"},
		Install:          []string{"echo hi"},
		Docker:           true,
		InstallTemplates: true,
		Startup:          []string{"echo boot"},
		Services:         []string{"web"},
		Masks: []MaskMount{
			{HostPath: "/var/devm/masks/p/web/data", MountTarget: "/Users/x/p/data", Owner: "devm"},
		},
		TimesyncdScript: "sudo tee /etc/systemd/timesyncd.conf.d/devm.conf > /dev/null <<'DEVM_TIMESYNCD'\nNTP=192.0.2.1\nDEVM_TIMESYNCD\n",
	}
	s := string(RenderProvisionOpenScript(in))

	// marker WRITTEN here (not cleaned — that's the enforced script's job)
	assert.Less(t, strings.Index(s, "touch /run/devm/provisioning"),
		strings.Index(s, "tar -x"))
	assert.NotContains(t, s, "rm -f /run/devm/provisioning")
	// fail-fast
	assert.Contains(t, s, "set -eo pipefail")
	// order: open BEFORE startup
	assert.Less(t, strings.Index(s, "::devm:stage:open::"),
		strings.Index(s, "::devm:stage:startup::"))
	// startup runs OPEN — no enforce/services/target content in this half
	assert.NotContains(t, s, "::devm:stage:enforce::")
	assert.NotContains(t, s, "::devm:stage:services::")
	assert.NotContains(t, s, "systemctl start devm.target")
	assert.NotContains(t, s, "systemctl start web.service")
	assert.NotContains(t, s, "touch /var/lib/devm/provisioned")
	// no mask/enforcement content leaks into the open half
	assert.NotContains(t, s, "mount --bind")
	assert.NotContains(t, s, "/etc/systemd/timesyncd.conf.d/devm.conf")
	// templates dispatcher runs through the wrapper, in the open window
	assert.Contains(t, s, "/opt/devm/scripts/with-devm-env bash /opt/devm/scripts/install-templates.sh")
	// install commands run through the with-devm-env wrapper (correct path)
	assert.Contains(t, s, "/opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo hi'")
	// docker feature installs the runc-shim runtime via daemon.json
	assert.Contains(t, s, "/etc/docker/daemon.json")
	assert.Contains(t, s, "devm-runc-shim")
	// stage markers present for the long-running steps
	for _, st := range []string{"packages", "install", "docker", "templates", "startup"} {
		assert.Contains(t, s, "::devm:stage:"+st+"::")
	}
	// install: commands are individually timeout-wrapped
	assert.Contains(t, s, "timeout 600 /opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo hi'")
	// startup: runs under one aggregate timeout budget for the script
	assert.Contains(t, s, "timeout 600 /opt/devm/scripts/with-devm-env bash /opt/devm/startup.sh")
}

func TestRenderProvisionOpenScript_NoOpenWindowWhenNothingOpen(t *testing.T) {
	// restart, empty startup, no packages/install/docker/templates → no
	// open-stage work and no first-boot marker.
	s := string(RenderProvisionOpenScript(ProvisionScriptInput{
		FirstBoot:       false,
		TimesyncdScript: "sudo tee /etc/systemd/timesyncd.conf.d/devm.conf > /dev/null <<'DEVM_TIMESYNCD'\nDEVM_TIMESYNCD\n",
	}))
	assert.NotContains(t, s, "::devm:stage:startup::")
	assert.NotContains(t, s, "::devm:stage:open::")
	// not first boot → no completion-marker write
	assert.NotContains(t, s, "touch /var/lib/devm/provisioned")
	// the guest-nft flush is UNCONDITIONAL — the base image's policy-drop
	// lock must be cleared every boot even when no open-stage work runs,
	// or a leftover policy-drop would drop softnet's own egress.
	assert.Contains(t, s, "sudo nft flush ruleset")
	// enforcement/NTP/target/marker-cleanup are the enforced script's job,
	// not rendered here at all.
	assert.NotContains(t, s, "systemctl start devm.target")
	assert.NotContains(t, s, "/etc/systemd/timesyncd.conf.d/devm.conf")
}

// TestRenderProvisionOpenScript_NftFlushUnconditionalAndBeforeOpenStage pins
// that the guest-nft flush runs BEFORE the open-stage work (so any install/
// startup/template steps that need egress see it already cleared) and
// happens regardless of hasOpenWork() — the flush and the open-stage work
// are independent now.
func TestRenderProvisionOpenScript_NftFlushUnconditionalAndBeforeOpenStage(t *testing.T) {
	s := string(RenderProvisionOpenScript(ProvisionScriptInput{
		FirstBoot:        true,
		InstallTemplates: true,
	}))
	flushIdx := strings.Index(s, "sudo nft flush ruleset")
	require.Greater(t, flushIdx, 0, "flush must be present")
	assert.Less(t, flushIdx, strings.Index(s, "::devm:stage:open::"),
		"flush must run before the open-stage work begins")
}

// TestRenderProvisionOpenScript_StepTimeoutOverride pins that a non-default
// StepTimeoutSeconds replaces the hardcoded 600s default in both the
// install: and startup: `timeout` wrapping — the daemon threads
// DEVM_INSTALL_STEP_TIMEOUT_S through Provisioner into this field, and
// e2e/test_75_install_step_timeout.py depends on it actually taking effect.
func TestRenderProvisionOpenScript_StepTimeoutOverride(t *testing.T) {
	s := string(RenderProvisionOpenScript(ProvisionScriptInput{
		FirstBoot:          true,
		Install:            []string{"echo hi"},
		Startup:            []string{"echo boot"},
		StepTimeoutSeconds: 1,
	}))
	assert.Contains(t, s, "timeout 1 /opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo hi'")
	assert.Contains(t, s, "timeout 1 /opt/devm/scripts/with-devm-env bash /opt/devm/startup.sh")
	assert.NotContains(t, s, "timeout 600 ")
}

func TestRenderProvisionOpenScript_RestartWithTemplatesOpensWindow(t *testing.T) {
	// A warm restart that still has templates must open the egress window so
	// a template installer that fetches over the network can run.
	s := string(RenderProvisionOpenScript(ProvisionScriptInput{
		FirstBoot:        false,
		InstallTemplates: true,
	}))
	assert.Contains(t, s, "::devm:stage:open::")
	assert.Contains(t, s, "::devm:stage:templates::")
	// but no first-boot-only work
	assert.NotContains(t, s, "::devm:stage:packages::")
	assert.NotContains(t, s, "::devm:stage:docker::")
}

func TestRenderProvisionOpen_InstallScriptRef_Expands(t *testing.T) {
	in := ProvisionScriptInput{
		FirstBoot: true,
		Install:   []string{"echo raw", ">install-supabase", "echo trailing"},
		Scripts: map[string][]string{
			"install-supabase": {"TAG=v1", "echo $TAG"},
		},
		StepTimeoutSeconds: 1,
	}
	s := string(RenderProvisionOpenScript(in))
	// Raw entries render unchanged.
	assert.Contains(t, s, "timeout 1 /opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo raw'")
	assert.Contains(t, s, "timeout 1 /opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo trailing'")
	// The ref expands to a single bash -c with commands joined by " && ".
	assert.Contains(t, s, `timeout 1 /opt/devm/scripts/with-devm-env bash -eo pipefail -c 'TAG=v1 && echo $TAG'`)
	// Progress markers: three steps total (raw + ref + raw).
	assert.Contains(t, s, "::devm:progress:install:1:3::")
	assert.Contains(t, s, "::devm:progress:install:2:3::")
	assert.Contains(t, s, "::devm:progress:install:3:3::")
}

func TestRenderProvisionEnforcedScript_Structure(t *testing.T) {
	in := ProvisionScriptInput{
		FirstBoot: true,
		Services:  []string{"web"},
		Masks: []MaskMount{
			{HostPath: "/var/devm/masks/p/web/data", MountTarget: "/Users/x/p/data", Owner: "devm"},
		},
		TimesyncdScript: "sudo tee /etc/systemd/timesyncd.conf.d/devm.conf > /dev/null <<'DEVM_TIMESYNCD'\nNTP=192.0.2.1\nDEVM_TIMESYNCD\n",
	}
	s := string(RenderProvisionEnforcedScript(in))

	// marker CLEANED here, as the LAST line, after devm.target starts.
	assert.Greater(t, strings.LastIndex(s, "rm -f /run/devm/provisioning"),
		strings.Index(s, "systemctl start devm.target"))
	assert.NotContains(t, s, "touch /run/devm/provisioning")
	// fail-fast
	assert.Contains(t, s, "set -eo pipefail")
	// no open-window content in this half
	assert.NotContains(t, s, "::devm:stage:open::")
	assert.NotContains(t, s, "tar -x")
	assert.NotContains(t, s, "nft flush ruleset")
	// enforce BEFORE target; services BEFORE target
	assert.Less(t, strings.Index(s, "::devm:stage:enforce::"),
		strings.Index(s, "systemctl start devm.target"))
	assert.Less(t, strings.Index(s, "systemctl start web.service"),
		strings.Index(s, "systemctl start devm.target"))
	assert.Less(t, strings.Index(s, "::devm:stage:enforce::"),
		strings.Index(s, "::devm:stage:services::"))
	// mask overlay: chown BEFORE the bind mount, mounted at the workspace path
	chownIdx := strings.Index(s, "chown devm '/var/devm/masks/p/web/data'")
	mountIdx := strings.Index(s, "mount --bind '/var/devm/masks/p/web/data' '/Users/x/p/data'")
	assert.Greater(t, chownIdx, 0)
	assert.Greater(t, mountIdx, chownIdx)
	// first-boot completion marker written before the target
	assert.Less(t, strings.Index(s, "touch /var/lib/devm/provisioned"),
		strings.Index(s, "systemctl start devm.target"))
	// timesyncd config lands in the enforce phase, AFTER the enforce stage
	// marker and BEFORE services/target come up.
	timesyncdIdx := strings.Index(s, "/etc/systemd/timesyncd.conf.d/devm.conf")
	require.Greater(t, timesyncdIdx, 0)
	assert.Greater(t, timesyncdIdx, strings.Index(s, "::devm:stage:enforce::"))
	assert.Less(t, timesyncdIdx, strings.Index(s, "::devm:stage:services::"))
	assert.Less(t, timesyncdIdx, strings.Index(s, "systemctl start devm.target"))
	// service health check is a bounded poll (is-active AND is-failed),
	// not a single is-failed snapshot — before the target.
	assert.Contains(t, s, "systemctl is-active --quiet web.service")
	assert.Contains(t, s, "systemctl is-failed --quiet web.service")
	assert.Less(t, strings.Index(s, "systemctl is-active --quiet web.service"),
		strings.Index(s, "systemctl start devm.target"))
}

func TestRenderProvisionEnforcedScript_NotFirstBoot_NoCompletionMarker(t *testing.T) {
	s := string(RenderProvisionEnforcedScript(ProvisionScriptInput{
		FirstBoot:       false,
		TimesyncdScript: "sudo tee /etc/systemd/timesyncd.conf.d/devm.conf > /dev/null <<'DEVM_TIMESYNCD'\nDEVM_TIMESYNCD\n",
	}))
	assert.NotContains(t, s, "touch /var/lib/devm/provisioned")
	// enforcement + target still happen every boot
	assert.Contains(t, s, "::devm:stage:enforce::")
	assert.Contains(t, s, "systemctl start devm.target")
	// timesyncd is applied every boot too, not just when the open window
	// ran — NTP must work on a warm restart as well.
	assert.Contains(t, s, "/etc/systemd/timesyncd.conf.d/devm.conf")
	// marker cleanup still happens even with no first-boot work.
	assert.Contains(t, s, "rm -f /run/devm/provisioning")
}

// TestRenderProvisionEnforcedScript_ServiceHealthPoll_OneShotAware pins that
// the health-check poll treats a oneshot unit that completed successfully
// (ActiveState=inactive, Result=success — never becomes "active") as
// healthy, not as a hang, alongside the plain is-active check used for
// simple/forking/notify services.
func TestRenderProvisionEnforcedScript_ServiceHealthPoll_OneShotAware(t *testing.T) {
	s := string(RenderProvisionEnforcedScript(ProvisionScriptInput{
		Services: []string{"migrate"},
	}))
	assert.Contains(t, s, `systemctl show -p Result --value migrate.service`)
	assert.Contains(t, s, `systemctl show -p ActiveState --value migrate.service`)
	assert.Contains(t, s, "success")
	assert.Contains(t, s, "inactive")
	// bounded — a deadline derived from SECONDS, not an unbounded loop.
	assert.Contains(t, s, "svc_deadline=$((SECONDS+10))")
	assert.Contains(t, s, `$SECONDS" -ge "$svc_deadline"`)
	// a failed unit aborts the whole script (loud, no access).
	assert.Contains(t, s, "echo 'service migrate failed' >&2; exit 1")
}
