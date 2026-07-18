package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderProvisionScript_Structure(t *testing.T) {
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
		EnforcedNft:     "table inet devm_filter { policy drop }",
		DnsmasqScript:   "sudo tee /etc/dnsmasq.d/devm.conf > /dev/null <<'DEVM_DNSMASQ'\nserver=192.168.64.1#53101\nDEVM_DNSMASQ\n",
		TimesyncdScript: "sudo tee /etc/systemd/timesyncd.conf.d/devm.conf > /dev/null <<'DEVM_TIMESYNCD'\nNTP=192.0.2.1\nDEVM_TIMESYNCD\n",
	}
	s := string(RenderProvisionScript(in))

	// marker first, delete last
	assert.Less(t, strings.Index(s, "touch /run/devm/provisioning"),
		strings.Index(s, "tar -x"))
	assert.Greater(t, strings.LastIndex(s, "rm -f /run/devm/provisioning"),
		strings.Index(s, "systemctl start devm.target"))
	// fail-fast
	assert.Contains(t, s, "set -eo pipefail")
	// order: open BEFORE startup, enforce BEFORE target
	assert.Less(t, strings.Index(s, "::devm:stage:open::"),
		strings.Index(s, "::devm:stage:startup::"))
	assert.Less(t, strings.Index(s, "::devm:stage:startup::"),
		strings.Index(s, "systemctl start devm.target"))
	assert.Less(t, strings.Index(s, "EnforcedNft-applied-marker"), // see enforce phase
		strings.Index(s, "systemctl start devm.target"))
	// startup runs OPEN (before the enforced nft is applied)
	assert.Less(t, strings.Index(s, "echo boot"),
		strings.Index(s, "EnforcedNft-applied-marker"))
	// services start BEFORE the target (access) — broken service must be loud
	assert.Less(t, strings.Index(s, "systemctl start web.service"),
		strings.Index(s, "systemctl start devm.target"))
	assert.Less(t, strings.Index(s, "::devm:stage:enforce::"),
		strings.Index(s, "::devm:stage:services::"))
	// mask overlay: chown BEFORE the bind mount, mounted at the workspace path
	chownIdx := strings.Index(s, "chown devm '/var/devm/masks/p/web/data'")
	mountIdx := strings.Index(s, "mount --bind '/var/devm/masks/p/web/data' '/Users/x/p/data'")
	assert.Greater(t, chownIdx, 0)
	assert.Greater(t, mountIdx, chownIdx)
	// templates dispatcher runs through the wrapper, in the open window
	assert.Contains(t, s, "/opt/devm/scripts/with-devm-env bash /opt/devm/scripts/install-templates.sh")
	// install commands run through the with-devm-env wrapper (correct path)
	assert.Contains(t, s, "/opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo hi'")
	// docker feature installs the runc-shim runtime via daemon.json
	assert.Contains(t, s, "/etc/docker/daemon.json")
	assert.Contains(t, s, "devm-runc-shim")
	// first-boot completion marker written before the target
	assert.Less(t, strings.Index(s, "touch /var/lib/devm/provisioned"),
		strings.Index(s, "systemctl start devm.target"))
	// stage markers present for the long-running steps
	for _, st := range []string{"packages", "install", "docker", "templates", "startup"} {
		assert.Contains(t, s, "::devm:stage:"+st+"::")
	}
	// install: commands are individually timeout-wrapped
	assert.Contains(t, s, "timeout 600 /opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo hi'")
	// startup: runs under one aggregate timeout budget for the script
	assert.Contains(t, s, "timeout 600 /opt/devm/scripts/with-devm-env bash /opt/devm/startup.sh")
	// dnsmasq + timesyncd config land in the enforce phase, AFTER the
	// enforced nft ruleset and BEFORE services/target come up.
	dnsmasqIdx := strings.Index(s, "/etc/dnsmasq.d/devm.conf")
	timesyncdIdx := strings.Index(s, "/etc/systemd/timesyncd.conf.d/devm.conf")
	require.Greater(t, dnsmasqIdx, 0)
	require.Greater(t, timesyncdIdx, 0)
	assert.Greater(t, dnsmasqIdx, strings.Index(s, "EnforcedNft-applied-marker"))
	assert.Greater(t, timesyncdIdx, strings.Index(s, "EnforcedNft-applied-marker"))
	assert.Less(t, dnsmasqIdx, strings.Index(s, "::devm:stage:services::"))
	assert.Less(t, timesyncdIdx, strings.Index(s, "::devm:stage:services::"))
	assert.Less(t, dnsmasqIdx, strings.Index(s, "systemctl start devm.target"))
	assert.Less(t, timesyncdIdx, strings.Index(s, "systemctl start devm.target"))
	// service health check is a bounded poll (is-active AND is-failed),
	// not a single is-failed snapshot — before the target.
	assert.Contains(t, s, "systemctl is-active --quiet web.service")
	assert.Contains(t, s, "systemctl is-failed --quiet web.service")
	assert.Less(t, strings.Index(s, "systemctl is-active --quiet web.service"),
		strings.Index(s, "systemctl start devm.target"))
}

func TestRenderProvisionScript_NoOpenWindowWhenNothingOpen(t *testing.T) {
	// restart, empty startup, no packages/install/docker/templates → no
	// open-stage work and no first-boot marker.
	s := string(RenderProvisionScript(ProvisionScriptInput{
		FirstBoot:       false,
		EnforcedNft:     "table inet devm_filter { policy drop }",
		DnsmasqScript:   "sudo tee /etc/dnsmasq.d/devm.conf > /dev/null <<'DEVM_DNSMASQ'\nDEVM_DNSMASQ\n",
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
	// enforcement + target still happen every boot
	assert.Contains(t, s, "EnforcedNft-applied-marker")
	assert.Contains(t, s, "systemctl start devm.target")
	// dnsmasq + timesyncd are applied every boot too, not just when the
	// open window runs — DNS/NTP must work on a warm restart as well.
	assert.Contains(t, s, "/etc/dnsmasq.d/devm.conf")
	assert.Contains(t, s, "/etc/systemd/timesyncd.conf.d/devm.conf")
}

// TestRenderProvisionScript_NftFlushUnconditionalAndBeforeOpenStage pins
// that the guest-nft flush runs BEFORE the open-stage work (so any install/
// startup/template steps that need egress see it already cleared) and
// happens regardless of hasOpenWork() — the flush and the open-stage work
// are independent now.
func TestRenderProvisionScript_NftFlushUnconditionalAndBeforeOpenStage(t *testing.T) {
	s := string(RenderProvisionScript(ProvisionScriptInput{
		FirstBoot:        true,
		InstallTemplates: true,
		EnforcedNft:      "table inet devm_filter { policy drop }",
	}))
	flushIdx := strings.Index(s, "sudo nft flush ruleset")
	require.Greater(t, flushIdx, 0, "flush must be present")
	assert.Less(t, flushIdx, strings.Index(s, "::devm:stage:open::"),
		"flush must run before the open-stage work begins")
}

// TestRenderProvisionScript_ServiceHealthPoll_OneShotAware pins that the
// health-check poll treats a oneshot unit that completed successfully
// (ActiveState=inactive, Result=success — never becomes "active") as
// healthy, not as a hang, alongside the plain is-active check used for
// simple/forking/notify services.
func TestRenderProvisionScript_ServiceHealthPoll_OneShotAware(t *testing.T) {
	s := string(RenderProvisionScript(ProvisionScriptInput{
		Services:    []string{"migrate"},
		EnforcedNft: "table inet devm_filter { policy drop }",
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

// TestRenderProvisionScript_StepTimeoutOverride pins that a non-default
// StepTimeoutSeconds replaces the hardcoded 600s default in both the
// install: and startup: `timeout` wrapping — the daemon threads
// DEVM_INSTALL_STEP_TIMEOUT_S through Provisioner into this field, and
// e2e/test_75_install_step_timeout.py depends on it actually taking effect.
func TestRenderProvisionScript_StepTimeoutOverride(t *testing.T) {
	s := string(RenderProvisionScript(ProvisionScriptInput{
		FirstBoot:          true,
		Install:            []string{"echo hi"},
		Startup:            []string{"echo boot"},
		EnforcedNft:        "table inet devm_filter { policy drop }",
		StepTimeoutSeconds: 1,
	}))
	assert.Contains(t, s, "timeout 1 /opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo hi'")
	assert.Contains(t, s, "timeout 1 /opt/devm/scripts/with-devm-env bash /opt/devm/startup.sh")
	assert.NotContains(t, s, "timeout 600 ")
}

func TestRenderProvisionScript_RestartWithTemplatesOpensWindow(t *testing.T) {
	// A warm restart that still has templates must open the egress window so
	// a template installer that fetches over the network can run.
	s := string(RenderProvisionScript(ProvisionScriptInput{
		FirstBoot:        false,
		InstallTemplates: true,
		EnforcedNft:      "table inet devm_filter { policy drop }",
	}))
	assert.Contains(t, s, "::devm:stage:open::")
	assert.Contains(t, s, "::devm:stage:templates::")
	// but no first-boot-only work
	assert.NotContains(t, s, "::devm:stage:packages::")
	assert.NotContains(t, s, "::devm:stage:docker::")
}
