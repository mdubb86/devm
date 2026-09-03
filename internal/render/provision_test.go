package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderProvisionBundleScript_Structure(t *testing.T) {
	s := string(RenderProvisionBundleScript(ProvisionScriptInput{FirstBoot: true}))

	// marker WRITTEN here (not cleaned — that's the enforced script's job)
	assert.Less(t, strings.Index(s, "touch /run/devm/provisioning"),
		strings.Index(s, "tar -x"))
	assert.NotContains(t, s, "rm -f /run/devm/provisioning")
	// fail-fast
	assert.Contains(t, s, "set -eo pipefail")
	// bundle stage marker present, so the daemon can drive a spinner /
	// classify a bundle-extract failure as teardown-class.
	assert.Contains(t, s, "::devm:stage:bundle::")
	// bundle extract + install.sh always run, unconditionally.
	assert.Contains(t, s, "sudo mkdir -p /opt/devm")
	assert.Contains(t, s, "sudo tar -xC /opt/devm")
	assert.Contains(t, s, "sudo /opt/devm/install.sh")
	// the guest-nft flush is unconditional too.
	assert.Contains(t, s, "sudo nft flush ruleset")
	// no user-phase content in this half at all.
	assert.NotContains(t, s, "::devm:stage:open::")
	assert.NotContains(t, s, "::devm:stage:packages::")
	assert.NotContains(t, s, "::devm:stage:install::")
	assert.NotContains(t, s, "::devm:stage:docker::")
	assert.NotContains(t, s, "::devm:stage:templates::")
	assert.NotContains(t, s, "::devm:stage:startup::")
	// no enforce/services/target content either.
	assert.NotContains(t, s, "::devm:stage:enforce::")
	assert.NotContains(t, s, "::devm:stage:services::")
	assert.NotContains(t, s, "systemctl start devm.target")
	assert.NotContains(t, s, "touch /var/lib/devm/provisioned")
}

// TestRenderProvisionBundleScript_UnconditionalRegardlessOfInput pins that
// the bundle-extract prologue and nft flush run every boot, regardless of
// FirstBoot/Startup/Packages/etc — install.sh is idempotent, and the flush
// must clear the base image's policy-drop lock even on a boot with zero
// user-phase work.
func TestRenderProvisionBundleScript_UnconditionalRegardlessOfInput(t *testing.T) {
	s := string(RenderProvisionBundleScript(ProvisionScriptInput{FirstBoot: false}))
	assert.Contains(t, s, "sudo tar -xC /opt/devm")
	assert.Contains(t, s, "sudo /opt/devm/install.sh")
	assert.Contains(t, s, "sudo nft flush ruleset")
	flushIdx := strings.Index(s, "sudo nft flush ruleset")
	extractIdx := strings.Index(s, "sudo tar -xC /opt/devm")
	require.Greater(t, flushIdx, 0)
	require.Greater(t, extractIdx, 0)
	assert.Less(t, extractIdx, flushIdx, "extract must run before the flush")
}

func TestRenderProvisionBundleScript_MacTimezone_EmittedWhenSet(t *testing.T) {
	s := string(RenderProvisionBundleScript(ProvisionScriptInput{
		FirstBoot:   true,
		MacTimezone: "America/Chicago",
	}))
	assert.Contains(t, s, "sudo timedatectl set-timezone 'America/Chicago'",
		"timezone override must be applied when MacTimezone is set")
}

func TestRenderProvisionBundleScript_MacTimezone_OmittedWhenEmpty(t *testing.T) {
	s := string(RenderProvisionBundleScript(ProvisionScriptInput{FirstBoot: true}))
	assert.NotContains(t, s, "timedatectl",
		"empty MacTimezone must leave the guest at UTC — no timedatectl call")
}

func TestRenderProvisionBundleScript_MacTimezone_ShellQuoted(t *testing.T) {
	// Zone names never contain quotes in practice, but the render must
	// still shell-quote to guarantee bash-safe args regardless of input.
	s := string(RenderProvisionBundleScript(ProvisionScriptInput{
		FirstBoot:   true,
		MacTimezone: "Etc/GMT+5",
	}))
	assert.Contains(t, s, "'Etc/GMT+5'",
		"MacTimezone must be single-quoted so `+` and any other bash-special char is inert")
}

func TestRenderProvisionUserScript_Structure(t *testing.T) {
	in := ProvisionScriptInput{
		FirstBoot:        true,
		Packages:         []string{"jq"},
		Install:          []string{"echo hi"},
		Docker:           true,
		InstallTemplates: true,
		Startup:          []string{"echo boot"},
		Services:         []string{"web"},
	}
	s := string(RenderProvisionUserScript(in))

	// fail-fast
	assert.Contains(t, s, "set -eo pipefail")
	// no bundle-extract content in this half at all — that's
	// RenderProvisionBundleScript's job now.
	assert.NotContains(t, s, "tar -xC /opt/devm")
	assert.NotContains(t, s, "/opt/devm/install.sh")
	assert.NotContains(t, s, "nft flush ruleset")
	assert.NotContains(t, s, "touch /run/devm/provisioning")
	// order: open BEFORE startup
	assert.Less(t, strings.Index(s, "::devm:stage:open::"),
		strings.Index(s, "::devm:stage:startup::"))
	// startup runs OPEN — no enforce/services/target content in this half
	assert.NotContains(t, s, "::devm:stage:enforce::")
	assert.NotContains(t, s, "::devm:stage:services::")
	assert.NotContains(t, s, "systemctl start devm.target")
	assert.NotContains(t, s, "systemctl start web.service")
	assert.NotContains(t, s, "touch /var/lib/devm/provisioned")
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

func TestRenderProvisionUserScript_NoOpenWindowWhenNothingOpen(t *testing.T) {
	// restart, empty startup, no packages/install/docker/templates → no
	// open-stage work and no first-boot marker.
	s := string(RenderProvisionUserScript(ProvisionScriptInput{
		FirstBoot: false,
	}))
	assert.NotContains(t, s, "::devm:stage:startup::")
	assert.NotContains(t, s, "::devm:stage:open::")
	// not first boot → no completion-marker write
	assert.NotContains(t, s, "touch /var/lib/devm/provisioned")
	// enforcement/target/marker-cleanup are the enforced script's job,
	// not rendered here at all.
	assert.NotContains(t, s, "systemctl start devm.target")
}

// TestRenderProvisionUserScript_StepTimeoutOverride pins that a non-default
// StepTimeoutSeconds replaces the hardcoded 600s default in both the
// install: and startup: `timeout` wrapping — the daemon threads
// DEVM_INSTALL_STEP_TIMEOUT_S through Provisioner into this field, and
// e2e/test_75_install_step_timeout.py depends on it actually taking effect.
func TestRenderProvisionUserScript_StepTimeoutOverride(t *testing.T) {
	s := string(RenderProvisionUserScript(ProvisionScriptInput{
		FirstBoot:          true,
		Install:            []string{"echo hi"},
		Startup:            []string{"echo boot"},
		StepTimeoutSeconds: 1,
	}))
	assert.Contains(t, s, "timeout 1 /opt/devm/scripts/with-devm-env bash -eo pipefail -c 'echo hi'")
	assert.Contains(t, s, "timeout 1 /opt/devm/scripts/with-devm-env bash /opt/devm/startup.sh")
	assert.NotContains(t, s, "timeout 600 ")
}

func TestRenderProvisionUserScript_RestartWithTemplatesOpensWindow(t *testing.T) {
	// A warm restart that still has templates must open the egress window so
	// a template installer that fetches over the network can run.
	s := string(RenderProvisionUserScript(ProvisionScriptInput{
		FirstBoot:        false,
		InstallTemplates: true,
	}))
	assert.Contains(t, s, "::devm:stage:open::")
	assert.Contains(t, s, "::devm:stage:templates::")
	// but no first-boot-only work
	assert.NotContains(t, s, "::devm:stage:packages::")
	assert.NotContains(t, s, "::devm:stage:docker::")
}

func TestRenderProvisionUser_InstallScriptRef_Expands(t *testing.T) {
	in := ProvisionScriptInput{
		FirstBoot: true,
		Install:   []string{"echo raw", ">install-supabase", "echo trailing"},
		Scripts: map[string][]string{
			"install-supabase": {"TAG=v1", "echo $TAG"},
		},
		StepTimeoutSeconds: 1,
	}
	s := string(RenderProvisionUserScript(in))
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

func TestProvisionUser_PackageConvergeOnNonFirstBoot(t *testing.T) {
	in := ProvisionScriptInput{
		FirstBoot:      false,
		PackageAdds:    []string{"sl"},
		PackageRemoves: []string{"chromium"},
	}
	s := string(RenderProvisionUserScript(in))
	// Converge renders inside the open window:
	require.Contains(t, s, "::devm:stage:packages::")
	require.Contains(t, s, "install -y 'sl'")
	require.Contains(t, s, "remove -y 'chromium'")
}

func TestProvisionUser_ConvergeAloneOpensWindow(t *testing.T) {
	// FirstBoot=false, no startup, no templates, only PackageAdds:
	// hasOpenWork must be true (the converge needs the open window).
	s := string(RenderProvisionUserScript(ProvisionScriptInput{
		FirstBoot:   false,
		PackageAdds: []string{"sl"},
	}))
	assert.Contains(t, s, "::devm:stage:open::")
	assert.Contains(t, s, "::devm:stage:packages::")
	assert.Contains(t, s, "install -y 'sl'")
}

func TestProvisionUser_FirstBootIgnoresConvergeFields(t *testing.T) {
	// FirstBoot=true renders the full Packages list exactly as today;
	// PackageAdds/Removes are not rendered (first boot has no drift).
	s := string(RenderProvisionUserScript(ProvisionScriptInput{
		FirstBoot:      true,
		Packages:       []string{"jq"},
		PackageAdds:    []string{"sl"},
		PackageRemoves: []string{"chromium"},
	}))
	assert.Contains(t, s, "apt_run install -y 'jq'")
	assert.NotContains(t, s, "install -y 'sl'")
	assert.NotContains(t, s, "remove -y 'chromium'")
}

func TestRenderProvisionEnforcedScript_Structure(t *testing.T) {
	in := ProvisionScriptInput{
		FirstBoot: true,
		Services:  []string{"web"},
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
	// first-boot completion marker written before the target
	assert.Less(t, strings.Index(s, "touch /var/lib/devm/provisioned"),
		strings.Index(s, "systemctl start devm.target"))
	// timesyncd is baked into the base image now (image/provision-base.sh)
	// — no timesyncd content is rendered into the composed script at all.
	assert.NotContains(t, s, "/etc/systemd/timesyncd.conf.d/devm.conf")
	// service health check is a bounded poll (is-active AND is-failed),
	// not a single is-failed snapshot — before the target.
	assert.Contains(t, s, "systemctl is-active --quiet web.service")
	assert.Contains(t, s, "systemctl is-failed --quiet web.service")
	assert.Less(t, strings.Index(s, "systemctl is-active --quiet web.service"),
		strings.Index(s, "systemctl start devm.target"))
}

func TestRenderProvisionEnforcedScript_NotFirstBoot_NoCompletionMarker(t *testing.T) {
	s := string(RenderProvisionEnforcedScript(ProvisionScriptInput{
		FirstBoot: false,
	}))
	assert.NotContains(t, s, "touch /var/lib/devm/provisioned")
	// enforcement + target still happen every boot
	assert.Contains(t, s, "::devm:stage:enforce::")
	assert.Contains(t, s, "systemctl start devm.target")
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

func TestRenderProvisionEnforcedScript_WritesGitCredentials(t *testing.T) {
	in := ProvisionScriptInput{
		GitCredentials: "https://x-access-token:__DEVM_SECRET_gh_token__@github.com/me/x.git\n",
		GitConfig:      "[credential]\n    helper = store\n    useHttpPath = true\n",
	}
	got := string(RenderProvisionEnforcedScript(in))

	// Both files provisioned atomically via install(1).
	assert.Contains(t, got,
		"install -o devm -g devm -m 0600 /dev/stdin /home/devm/.git-credentials",
		"credentials file must be written via install(1) with the correct mode+owner")
	assert.Contains(t, got,
		"install -o devm -g devm -m 0644 /dev/stdin /home/devm/.gitconfig",
		"gitconfig must be written via install(1) with the correct mode+owner")
	// Bodies embedded via heredoc (immune to shell quoting).
	assert.Contains(t, got,
		"https://x-access-token:__DEVM_SECRET_gh_token__@github.com/me/x.git",
		"credentials file body must appear in the emitted script")
	assert.Contains(t, got,
		"[credential]",
		"gitconfig body must appear in the emitted script")
}

func TestRenderProvisionEnforcedScript_NoGitFilesWhenBothEmpty(t *testing.T) {
	// When there are zero repo declarations, don't write empty files —
	// keeps the provisioning surface clean for projects that never git.
	in := ProvisionScriptInput{GitCredentials: "", GitConfig: ""}
	got := string(RenderProvisionEnforcedScript(in))
	assert.NotContains(t, got, "/home/devm/.git-credentials")
	assert.NotContains(t, got, "/home/devm/.gitconfig")
}

func TestRenderProvisionEnforcedScript_WritesGitconfigEvenWithEmptyCredentials(t *testing.T) {
	// Edge case: caller wants gitconfig but no credential lines (e.g. a
	// devm.yaml with `repo:` declarations that resolve to zero bindings
	// for some reason). Still write gitconfig — future git ops from the
	// user's own credential setup keep working through iron-proxy.
	in := ProvisionScriptInput{
		GitCredentials: "",
		GitConfig:      "[credential]\n    helper = store\n    useHttpPath = true\n",
	}
	got := string(RenderProvisionEnforcedScript(in))
	assert.Contains(t, got, "install -o devm -g devm -m 0644 /dev/stdin /home/devm/.gitconfig")
	assert.NotContains(t, got, "install -o devm -g devm -m 0600 /dev/stdin /home/devm/.git-credentials")
}
