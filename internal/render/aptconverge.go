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
	const apt = "sudo apt-get -o DPkg::Lock::Timeout=60"
	var b strings.Builder
	if len(adds) > 0 {
		fmt.Fprintf(&b, "%s update\n", apt)
		fmt.Fprintf(&b, "%s install -y %s\n", apt, joinQuoted(adds))
	}
	if len(removes) > 0 {
		fmt.Fprintf(&b, "%s remove -y %s\n", apt, joinQuoted(removes))
		fmt.Fprintf(&b, "%s autoremove -y\n", apt)
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
