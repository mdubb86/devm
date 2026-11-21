package render

import (
	"bytes"
	"fmt"

	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/softnet"
)

// SSHClientConfig renders the guest's /home/devm/.ssh/config body. One
// fixed Host devm-jump block pointing at the Mac-side jump via the
// softnet NAT alias, plus one Host <alias> block per resolved target.
// IdentityAgent on every target routes signing requests through the
// guest-side agent proxy at bridgeSockPath (served by
// gdevm ssh-agent-bridge).
func SSHClientConfig(jumpPort int, bridgeSockPath string, targets []schema.ResolvedSSHTarget) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Written by devm — do not edit; `devm reconcile` overwrites.\n\n")
	fmt.Fprintf(&b, "Host devm-jump\n")
	fmt.Fprintf(&b, "  HostName %s\n", softnet.NATAliasIP)
	fmt.Fprintf(&b, "  Port %d\n", jumpPort)
	fmt.Fprintf(&b, "  User devm\n")
	fmt.Fprintf(&b, "  IdentityFile ~/.ssh/devm-jump-id\n")
	fmt.Fprintf(&b, "  IdentitiesOnly yes\n")
	fmt.Fprintf(&b, "  PasswordAuthentication no\n\n")
	for _, t := range targets {
		fmt.Fprintf(&b, "Host %s\n", t.Alias)
		fmt.Fprintf(&b, "  HostName %s\n", t.HostName)
		fmt.Fprintf(&b, "  Port %d\n", t.Port)
		fmt.Fprintf(&b, "  User %s\n", t.User)
		if t.HostKeyAlias != "" {
			fmt.Fprintf(&b, "  HostKeyAlias %s\n", t.HostKeyAlias)
		}
		fmt.Fprintf(&b, "  ProxyJump devm-jump\n")
		fmt.Fprintf(&b, "  IdentityAgent %s\n", bridgeSockPath)
		fmt.Fprintf(&b, "  PreferredAuthentications publickey\n")
		fmt.Fprintf(&b, "  PasswordAuthentication no\n")
		fmt.Fprintf(&b, "  KbdInteractiveAuthentication no\n\n")
	}
	return b.Bytes()
}
