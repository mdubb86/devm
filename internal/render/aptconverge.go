package render

import (
	"fmt"
	"strings"
)

// AptConvergeScript returns the bash body that converges the guest's
// apt package set: install every package in adds, remove every package
// in removes, then autoremove now-orphaned auto-installed deps.
// Removal is plain remove (no --purge): config files persisting until
// teardown is acceptable for a sandbox. The DPkg lock timeout makes a
// concurrent in-guest apt a wait, not a spurious failure. Empty input
// yields an empty script.
func AptConvergeScript(adds, removes []string) string {
	if len(adds) == 0 && len(removes) == 0 {
		return ""
	}
	var b strings.Builder
	// A prior converge killed mid-dpkg-run leaves the dpkg database
	// locked in an inconsistent state; repair it first so this (or any
	// future) converge doesn't wedge on "dpkg was interrupted".
	b.WriteString("sudo dpkg --configure -a\n")
	// apt_run is defined at the top of the surrounding script by
	// AptRetryHelper — Acquire::Retries=3 + DPkg::Lock::Timeout=60 +
	// outer retry-with-backoff loop, so a transient mirror stall no
	// longer tears the VM down.
	if len(adds) > 0 {
		fmt.Fprintf(&b, "apt_run update\n")
		fmt.Fprintf(&b, "apt_run install -y %s\n", joinQuoted(adds))
	}
	if len(removes) > 0 {
		fmt.Fprintf(&b, "apt_run remove -y %s\n", joinQuoted(removes))
		fmt.Fprintf(&b, "apt_run autoremove -y\n")
	}
	return b.String()
}

// joinQuoted single-quotes each package name and joins them with a space
// for use as apt-get arguments.
func joinQuoted(pkgs []string) string {
	quoted := make([]string, len(pkgs))
	for i, pkg := range pkgs {
		quoted[i] = shellSingleQuoted(pkg)
	}
	return strings.Join(quoted, " ")
}
