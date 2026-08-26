package serviceapi

import (
	"crypto/ed25519"
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
