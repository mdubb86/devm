package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderProvisionScript_Structure(t *testing.T) {
	in := ProvisionScriptInput{
		FirstBoot:   true,
		Packages:    []string{"jq"},
		Install:     []string{"echo hi"},
		Docker:      true,
		Startup:     []string{"echo boot"},
		Services:    []string{"web"},
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
	assert.Less(t, strings.Index(s, "::devm:stage:startup::"),
		strings.Index(s, "systemctl start devm.target"))
	assert.Less(t, strings.Index(s, "EnforcedNft-applied-marker"), // see step 3
		strings.Index(s, "systemctl start devm.target"))
	// startup runs OPEN (before the enforced nft is applied)
	assert.Less(t, strings.Index(s, "echo boot"),
		strings.Index(s, "systemctl start devm.target"))
	// services start BEFORE the target (access) — broken service must be loud
	assert.Less(t, strings.Index(s, "systemctl start web.service"),
		strings.Index(s, "systemctl start devm.target"))
	// stage markers present for the long-running steps
	for _, st := range []string{"packages", "install", "docker", "startup"} {
		assert.Contains(t, s, "::devm:stage:"+st+"::")
	}
}

func TestRenderProvisionScript_NoOpenWindowWhenNothingOpen(t *testing.T) {
	// restart, empty startup, no packages/install/docker → no flush-to-allow-all
	s := string(RenderProvisionScript(ProvisionScriptInput{FirstBoot: false}))
	assert.NotContains(t, s, "::devm:stage:startup::")
	assert.NotContains(t, s, "OpenNft") // the open block is omitted
}
