// Package nftscript builds the nftables scripts for the svc_ingress chain
// (direct-service ingress) shared by the provisioner (cold-start) and
// reconcile's ApplyLive (warm reconcile). It is a leaf package — it must
// not import serviceapi or reconcile, since serviceapi already imports
// reconcile and reconcile needs these helpers, which would create a
// compile cycle if they lived in serviceapi.
package nftscript

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mdubb86/devm/internal/schema"
)

// BuildSvcIngressScript flush-rebuilds the svc_ingress chain from the
// given direct-service ports (the pre-DNAT, declared ports) and snapshots
// it to /etc/nftables.d/svc_ingress.conf. Idempotent: `add chain` ensures
// the chain exists before flush. Passing an empty slice closes all direct
// ingress. The `jump svc_ingress` into the forward hook is established by
// serviceapi's buildNftablesScript at cold-start; this only manages the
// chain contents. Called by the provisioner (cold start) and ApplyLive
// (warm reconcile) — the single source of truth for the chain contents.
func BuildSvcIngressScript(ports []int) string {
	var rules strings.Builder
	for _, p := range ports {
		fmt.Fprintf(&rules,
			"add rule inet devm_filter svc_ingress ct original proto-dst %d accept comment \"devm: direct ingress %d\"\n",
			p, p)
	}
	return fmt.Sprintf(`sudo nft -f - <<'EOF'
add table inet devm_filter
add chain inet devm_filter svc_ingress
flush chain inet devm_filter svc_ingress
%sEOF
sudo mkdir -p /etc/nftables.d
sudo sh -c 'nft list chain inet devm_filter svc_ingress > /etc/nftables.d/svc_ingress.conf'
`, rules.String())
}

// DirectPorts returns the declared ports of all direct services when the
// project uses docker (host-process direct services need no forward rule).
// Returns nil for non-docker projects, so callers get an empty svc_ingress.
func DirectPorts(cfg schema.Config) []int {
	if !cfg.Docker {
		return nil
	}
	var ports []int
	for _, svc := range cfg.Services {
		if svc.Direct && svc.Port != 0 {
			ports = append(ports, svc.Port)
		}
	}
	sort.Ints(ports)
	return ports
}
