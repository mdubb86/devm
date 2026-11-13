package serviceapi

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/mdubb86/devm/internal/devmbundle"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/softnet"
)

// guestSSHAgentSock is the path the guest-side ssh-agent bridge binds
// and that every target's IdentityAgent in the rendered ssh-client
// config points at.
const guestSSHAgentSock = "/run/devm/ssh-agent.sock"

// BindSSHBundleEnricher wires devmbundle.SSHEnricher to a function
// closed over proxy. RunService calls this once after creating the
// ProxyServer so every downstream BuildInputFor invocation can pick
// up SSH bundle bytes for a project whose cfg.project.ssh is non-
// empty, without having to thread a *ProxyServer through packages
// that don't own one.
func BindSSHBundleEnricher(proxy *ProxyServer) {
	devmbundle.SSHEnricher = func(cfg schema.Config, runtimeDir string) (devmbundle.SSHBundleFields, error) {
		return computeSSHBundleFields(cfg, runtimeDir, proxy)
	}
}

// computeSSHBundleFields resolves this project's ssh targets, loads
// the per-project jump key material, reads this project's jump +
// bridge ports from proxy, and renders the four SSH bundle bytes.
// cfg.project.ssh MUST be non-empty when this is called — the caller
// (BuildInputFor via SSHEnricher) gates on that.
func computeSSHBundleFields(cfg schema.Config, runtimeDir string, proxy *ProxyServer) (devmbundle.SSHBundleFields, error) {
	projectID := cfg.Project.Name
	resolved, err := schema.ResolveSSHTargets(*cfg.Project.SSH, schema.DefaultExec)
	if err != nil {
		return devmbundle.SSHBundleFields{}, fmt.Errorf("resolve ssh: %w", err)
	}
	snap := proxy.ProjectSnapshot(projectID)
	privBytes, _, err := LoadOrCreateProjectJumpKey(runtimeDir, projectID)
	if err != nil {
		return devmbundle.SSHBundleFields{}, fmt.Errorf("jump client key: %w", err)
	}
	hostSigner, err := LoadOrCreateJumpHostKey(runtimeDir)
	if err != nil {
		return devmbundle.SSHBundleFields{}, fmt.Errorf("jump host key: %w", err)
	}

	// Build known_hosts body: devm-jump host-key line first, then one
	// line per resolved target pulled from the Mac's ~/.ssh/known_hosts
	// via ssh-keygen -F. CheckKnownHosts has already verified each
	// target has at least one entry, but a transient ssh-keygen error
	// on an individual target just gets skipped — the guest will
	// StrictHostKeyChecking=ask on first reach, which is the same
	// behavior a Mac terminal would show.
	var khBuf bytes.Buffer
	// ssh.MarshalAuthorizedKey already terminates with '\n'.
	fmt.Fprintf(&khBuf, "[%s]:%d %s",
		softnet.NATAliasIP, snap.JumpPort,
		ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))
	for _, r := range resolved {
		out, err := schema.DefaultExec("ssh-keygen", "-F", r.HostName)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			khBuf.WriteString(line)
			khBuf.WriteByte('\n')
		}
	}

	return devmbundle.SSHBundleFields{
		ClientConfig:     render.SSHClientConfig(snap.JumpPort, guestSSHAgentSock, resolved),
		JumpClientPriv:   privBytes,
		KnownHostsAppend: khBuf.Bytes(),
		AgentBridgeUnit:  render.SSHAgentBridgeUnit(softnet.NATAliasIP, snap.BridgePort, guestSSHAgentSock),
	}, nil
}
