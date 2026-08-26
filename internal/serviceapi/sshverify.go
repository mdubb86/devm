package serviceapi

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
)

// sshVerifyAddr maps a ProjectIP to the endpoint the reconcile-time
// host-key verify dials. Var so tests can redirect :22 (root-only to
// bind on pool IPs) to fake sshd listeners on ephemeral ports.
var sshVerifyAddr = func(projectIP string) string {
	return net.JoinHostPort(projectIP, "22")
}

// verifySSHHostKey dials addr expecting an SSH server that presents
// expectedPub (authorized_keys format — the project's managed
// ssh_host_ed25519_key.pub). Auth is never attempted with real
// credentials; the handshake's key exchange alone proves which host
// key the listener holds. Returns nil on a match; a fingerprint-
// carrying "host key mismatch" error when a foreign SSH server
// answers; a handshake error when the listener isn't speaking SSH.
func verifySSHHostKey(addr string, expectedPub []byte, timeout time.Duration) error {
	want, _, _, _, err := ssh.ParseAuthorizedKey(expectedPub)
	if err != nil {
		return fmt.Errorf("parse expected host key: %w", err)
	}

	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	var presented ssh.PublicKey
	clientCfg := &ssh.ClientConfig{
		User: "devm",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			presented = key
			return nil // record and continue; the comparison happens below
		},
		Timeout: timeout,
	}
	// No auth methods: the handshake completes key exchange (running
	// the callback above), then fails authentication — which is fine,
	// the host key is all this probe is after.
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err == nil {
		// Auth unexpectedly succeeded (e.g. a server allowing "none");
		// the key was still captured.
		go ssh.DiscardRequests(reqs)
		go func() {
			for ch := range chans {
				_ = ch.Reject(ssh.Prohibited, "probe")
			}
		}()
		_ = c.Close()
	}
	if presented == nil {
		return fmt.Errorf("ssh handshake with %s failed before key exchange (listener is not this project's sshd): %v", addr, err)
	}
	if !bytes.Equal(presented.Marshal(), want.Marshal()) {
		return fmt.Errorf("host key mismatch at %s: listener presents %s, project expects %s — the address is answered by a foreign SSH server",
			addr, ssh.FingerprintSHA256(presented), ssh.FingerprintSHA256(want))
	}
	return nil
}

// healCrossWiredIP moves a project off a ProjectIP whose :22 is
// answered by a foreign listener: release the address, allocate a
// fresh one (the allocator's :22 probe skips the squatter), rebind the
// daemon's per-project proxy listeners on it, and re-push the ingress
// expose map so softnet closes the stale-BindIP listeners and binds
// the new ones. DNS follows ironProxyState automatically and the
// ssh_config/known_hosts material is IP-free, so nothing else moves.
// Returns the replacement IP.
func healCrossWiredIP(ctx context.Context, cfg identity.Config, projectID string, projCfg schema.Config, proxy *ProxyServer, ntpPort int) (string, error) {
	ReleaseProjectIP(cfg, projectID)
	newIP, err := AllocateProjectIP(cfg, projectID)
	if err != nil {
		return "", fmt.Errorf("reallocate project IP: %w", err)
	}
	if proxy != nil {
		if st := rebindProjectListeners(ctx, proxy, cfg, projectID, newIP, ntpPort); st.State != RebindOK {
			return "", fmt.Errorf("rebind listeners on %s: %s", newIP, st.LastError)
		}
	}
	if err := pushExposeMap(projectID, computeExposeMap(projCfg, newIP)); err != nil {
		return "", fmt.Errorf("re-push expose map on %s: %w", newIP, err)
	}
	return newIP, nil
}
