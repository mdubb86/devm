package serviceapi

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// fakeSSHD starts a minimal SSH server presenting the given host key
// and rejecting all auth. Returns its address.
func fakeSSHD(t *testing.T, hostPriv ssh.Signer) string {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, ssh.ErrNoAuth
		},
	}
	cfg.AddHostKey(hostPriv)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return // auth failure is the expected end state
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					_ = ch.Reject(ssh.Prohibited, "test server")
				}
				_ = conn.Close()
			}(c)
		}
	}()
	return ln.Addr().String()
}

func genSigner(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return signer, ssh.MarshalAuthorizedKey(sshPub)
}

// A server holding multiple host keys (the real guest sshd has
// ed25519 + ECDSA + RSA; devm manages only ed25519) must still verify:
// the probe pins negotiation to the expected key's algorithm so the
// server can't present a sibling key that compares as "foreign". This
// is the false-cross-wire the e2e caught — an un-pinned handshake
// negotiated a non-ed25519 key from a healthy guest and reconcile
// "healed" an IP that was never cross-wired.
func TestVerifySSHHostKey_MultiKeyServerMatchesManagedKey(t *testing.T) {
	edSigner, edPub := genSigner(t)
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecdsaSigner, err := ssh.NewSignerFromKey(ecdsaKey)
	require.NoError(t, err)

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, ssh.ErrNoAuth
		},
	}
	// ECDSA added FIRST so an un-pinned client that follows server
	// order would get the non-managed key.
	cfg.AddHostKey(ecdsaSigner)
	cfg.AddHostKey(edSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _, _, _ = ssh.NewServerConn(c, cfg)
			}(c)
		}
	}()

	require.NoError(t, verifySSHHostKey(ln.Addr().String(), edPub, 3*time.Second))
}

func TestVerifySSHHostKey_Match(t *testing.T) {
	signer, pub := genSigner(t)
	addr := fakeSSHD(t, signer)
	require.NoError(t, verifySSHHostKey(addr, pub, 3*time.Second))
}

func TestVerifySSHHostKey_Mismatch(t *testing.T) {
	signer, _ := genSigner(t)
	_, otherPub := genSigner(t)
	addr := fakeSSHD(t, signer)
	err := verifySSHHostKey(addr, otherPub, 3*time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "host key mismatch")
	require.Contains(t, err.Error(), "SHA256:", "error should carry fingerprints")
}

// A listener that accepts TCP but never speaks SSH (the raw-squatter
// shape) must report a handshake failure, not a mismatch.
func TestVerifySSHHostKey_NotSSH(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	_, pub := genSigner(t)
	err = verifySSHHostKey(ln.Addr().String(), pub, 1*time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "handshake")
	require.False(t, strings.Contains(err.Error(), "mismatch"))
}

func TestVerifySSHHostKey_Refused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	_, pub := genSigner(t)
	err = verifySSHHostKey(addr, pub, 1*time.Second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "dial")
}
