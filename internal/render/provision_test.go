package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
		SvcIngressPorts:  []int{54321},
		Masks: []MaskMount{
			{HostPath: "/var/devm/masks/p/web/data", MountTarget: "/Users/x/p/data", Owner: "devm"},
		},
		LockedNft:   "table inet devm_filter { }",
		OpenNft:     "flush ruleset", // allow-all for the open window
		EnforcedNft: "table inet devm_filter { policy drop }",
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
	// enforced nft is applied BEFORE svc_ingress, which is BEFORE masks/services
	assert.Less(t, strings.Index(s, "EnforcedNft-applied-marker"),
		strings.Index(s, "svc_ingress"))
	assert.Less(t, strings.Index(s, "::devm:stage:enforce::"),
		strings.Index(s, "::devm:stage:services::"))
	// svc_ingress opens the declared direct port
	assert.Contains(t, s, "ct original proto-dst 54321 accept")
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
}

func TestRenderProvisionScript_NoOpenWindowWhenNothingOpen(t *testing.T) {
	// restart, empty startup, no packages/install/docker/templates → no
	// flush-to-allow-all and no first-boot marker.
	s := string(RenderProvisionScript(ProvisionScriptInput{
		FirstBoot:   false,
		EnforcedNft: "table inet devm_filter { policy drop }",
	}))
	assert.NotContains(t, s, "::devm:stage:startup::")
	assert.NotContains(t, s, "::devm:stage:open::")
	assert.NotContains(t, s, "OpenNft") // the open block is omitted
	// not first boot → no completion-marker write
	assert.NotContains(t, s, "touch /var/lib/devm/provisioned")
	// enforcement + target still happen every boot
	assert.Contains(t, s, "EnforcedNft-applied-marker")
	assert.Contains(t, s, "systemctl start devm.target")
}

func TestRenderProvisionScript_RestartWithTemplatesOpensWindow(t *testing.T) {
	// A warm restart that still has templates must open the egress window so
	// a template installer that fetches over the network can run.
	s := string(RenderProvisionScript(ProvisionScriptInput{
		FirstBoot:        false,
		InstallTemplates: true,
		OpenNft:          "flush ruleset",
		EnforcedNft:      "table inet devm_filter { policy drop }",
	}))
	assert.Contains(t, s, "::devm:stage:open::")
	assert.Contains(t, s, "::devm:stage:templates::")
	// but no first-boot-only work
	assert.NotContains(t, s, "::devm:stage:packages::")
	assert.NotContains(t, s, "::devm:stage:docker::")
}
