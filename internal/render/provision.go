package render

import (
	"fmt"
	"strings"
)

// ProvisionScriptInput is everything the composed guest provisioning script
// needs baked in. The daemon builds this from schema.Config + the rendered
// nft rule sets, then RenderProvisionScript turns it into one bash script.
type ProvisionScriptInput struct {
	FirstBoot   bool
	Packages    []string // apt packages (first boot only)
	Install     []string // install: commands (first boot only)
	Docker      bool     // run the docker feature (first boot only)
	Startup     []string // startup: commands (every boot)
	Services    []string // service unit names to enable+start (health-polled)
	LockedNft   string   // the locked skeleton (re-affirmed if no open work)
	OpenNft     string   // allow-all ruleset for the open window
	EnforcedNft string   // the real project allowlist
}

// hasOpenWork reports whether the open egress window is needed this boot.
func (in ProvisionScriptInput) hasOpenWork() bool {
	return in.FirstBoot || len(in.Startup) > 0
}

// RenderProvisionScript composes the single guest provisioning script. It is
// delivered as `bash -c '<this>'` with the bundle tar on stdin. Stages the CLI
// surfaces are marked with `::devm:stage:<name>::` on stdout; the daemon
// (Task 5/6) parses them to drive the spinner. `set -eo pipefail` makes any
// failing command abort before `systemctl start devm.target`, so a failure
// never grants access.
func RenderProvisionScript(in ProvisionScriptInput) []byte {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	p("#!/bin/bash")
	p("set -eo pipefail")
	// (1) in-progress marker FIRST. /run is tmpfs and /run/devm doesn't
	// exist yet; the script runs as the unprivileged devm user, so both
	// the directory and the marker need sudo.
	p("sudo mkdir -p /run/devm")
	p("sudo touch /run/devm/provisioning")
	// (2) extract the bundle (tar on stdin) and run the extractor for CA/symlinks
	p("sudo mkdir -p /opt/devm")
	p("sudo tar -xC /opt/devm")    // consumes stdin
	p("sudo /opt/devm/install.sh") // existing CA install + PATH symlink

	if in.hasOpenWork() {
		p("echo ::devm:stage:open::")
		p("sudo nft -f - <<'DEVM_OPEN_NFT'\n%s\nDEVM_OPEN_NFT", in.OpenNft)
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
					p("/opt/devm/with-devm-env bash -eo pipefail -c %s", shellSingleQuoted(cmd))
				}
			}
			if in.Docker {
				p("echo ::devm:stage:docker::")
				p("curl -fsSL https://get.docker.com | sudo sh")
				// dockerd joins the gate: disabled at boot, wanted by devm.target
				p("sudo systemctl disable docker.service")
				p("sudo mkdir -p /etc/systemd/system/devm.target.wants")
				p("sudo ln -sf /lib/systemd/system/docker.service /etc/systemd/system/devm.target.wants/docker.service")
			}
		}
		if len(in.Startup) > 0 {
			p("echo ::devm:stage:startup::")
			p("/opt/devm/with-devm-env bash /opt/devm/startup.sh")
		}
	}

	// (3) enforce: apply the real allowlist, then svc_ingress/masks are handled
	// by the daemon's enforce callback content baked into EnforcedNft.
	p("sudo nft -f - <<'DEVM_ENFORCE_NFT'\n%s\nDEVM_ENFORCE_NFT", in.EnforcedNft)
	p("# EnforcedNft-applied-marker")

	// (4) start + health-poll services BEFORE granting access. Services are
	// WantedBy=devm.target (disabled at boot), but we start them EXPLICITLY here
	// and poll — so a broken service aborts (exit 1) BEFORE ssh/the target come
	// up. This preserves the spec's ordering: "broken service is loud, no access".
	p("sudo systemctl daemon-reload")
	p("sudo systemctl unmask ssh")
	for _, svc := range in.Services {
		p("sudo systemctl enable %s.service", svc)
		p("sudo systemctl start %s.service", svc)
	}
	for _, svc := range in.Services {
		p("if systemctl is-failed --quiet %s.service; then echo 'service %s failed' >&2; exit 1; fi", svc, svc)
	}

	// (5) LAST functional line: activate the target — ssh + caddy + dnsmasq +
	// dockerd come up (services already healthy), enforced. Access is granted
	// ONLY after services are confirmed up.
	p("sudo systemctl start devm.target")
	// (6) cleanup marker on success
	p("sudo rm -f /run/devm/provisioning")

	return []byte(b.String())
}
