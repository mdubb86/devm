package serviceapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mdubb86/devm/internal/schema"
)

func TestJumpAllowlist(t *testing.T) {
	a := NewJumpAllowlist([]schema.ResolvedSSHTarget{
		{Alias: "prod", HostName: "ops.example.com", Port: 22},
		{Alias: "home", HostName: "home.net", Port: 2222},
	})
	cases := []struct {
		host string
		port int
		want bool
	}{
		{"ops.example.com", 22, true},
		{"home.net", 2222, true},
		{"ops.example.com", 2222, false}, // right host wrong port
		{"home.net", 22, false},
		{"other.host", 22, false},
		{"", 22, false},
	}
	for _, c := range cases {
		if got := a.Contains(c.host, c.port); got != c.want {
			t.Errorf("Contains(%q,%d) = %v want %v", c.host, c.port, got, c.want)
		}
	}
}

func TestJumpServer_AllowedTargetSucceeds(t *testing.T) {
	// 1. Start a fake "target" TCP server that echoes exactly 5 bytes.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()
	go func() {
		c, _ := targetLn.Accept()
		if c == nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 5)
		n, _ := c.Read(buf)
		c.Write(buf[:n])
	}()
	targetPort := targetLn.Addr().(*net.TCPAddr).Port

	// 2. Build a jump server with that target in its allowlist.
	hostKey := testGenHostKey(t)
	clientPrivPEM, clientPub := testGenClientKey(t)
	allow := NewJumpAllowlist([]schema.ResolvedSSHTarget{
		{Alias: "target", HostName: "127.0.0.1", Port: targetPort},
	})
	s := NewJumpServer(hostKey, clientPub, allow, "projX")

	jumpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer jumpLn.Close()
	go func() {
		for {
			c, err := jumpLn.Accept()
			if err != nil {
				return
			}
			go s.ServeConn(c)
		}
	}()
	jumpPort := jumpLn.Addr().(*net.TCPAddr).Port

	// 3. SSH-client-to-jump, then open direct-tcpip.
	clientSigner, err := ssh.ParsePrivateKey(clientPrivPEM)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(jumpPort)), &ssh.ClientConfig{
		User:            "devm",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey()),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	conn, err := client.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(targetPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = conn.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := conn.Read(buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("got %q", buf)
	}
}

func TestJumpServer_NotAllowedRejects(t *testing.T) {
	// allowlist is empty
	hostKey := testGenHostKey(t)
	clientPrivPEM, clientPub := testGenClientKey(t)
	allow := NewJumpAllowlist(nil)
	s := NewJumpServer(hostKey, clientPub, allow, "projX")

	jumpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	defer jumpLn.Close()
	go func() {
		for {
			c, err := jumpLn.Accept()
			if err != nil {
				return
			}
			go s.ServeConn(c)
		}
	}()
	jumpPort := jumpLn.Addr().(*net.TCPAddr).Port

	clientSigner, _ := ssh.ParsePrivateKey(clientPrivPEM)
	client, err := ssh.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(jumpPort)), &ssh.ClientConfig{
		User:            "devm",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey()),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, err = client.Dial("tcp", "127.0.0.1:12345")
	if err == nil {
		t.Fatal("want reject, got success")
	}
	var ocf *ssh.OpenChannelError
	if !errors.As(err, &ocf) || ocf.Reason != ssh.Prohibited {
		t.Fatalf("want Prohibited OpenChannelError, got %v", err)
	}
}

func TestJumpServer_RejectsSessionChannel(t *testing.T) {
	hostKey := testGenHostKey(t)
	clientPrivPEM, clientPub := testGenClientKey(t)
	s := NewJumpServer(hostKey, clientPub, NewJumpAllowlist(nil), "projX")

	jumpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	defer jumpLn.Close()
	go func() {
		for {
			c, err := jumpLn.Accept()
			if err != nil {
				return
			}
			go s.ServeConn(c)
		}
	}()
	jumpPort := jumpLn.Addr().(*net.TCPAddr).Port

	clientSigner, _ := ssh.ParsePrivateKey(clientPrivPEM)
	client, err := ssh.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(jumpPort)), &ssh.ClientConfig{
		User:            "devm",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey()),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, _, err = client.OpenChannel("session", nil)
	if err == nil {
		t.Fatal("want reject on session channel")
	}
	var ocf *ssh.OpenChannelError
	if !errors.As(err, &ocf) || ocf.Reason != ssh.Prohibited {
		t.Fatalf("want Prohibited, got %v", err)
	}
}

func TestLoadOrCreateJumpHostKey_PersistsAndReturnsSame(t *testing.T) {
	dir := t.TempDir()
	s1, err := LoadOrCreateJumpHostKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := LoadOrCreateJumpHostKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(s1.PublicKey().Marshal()) != string(s2.PublicKey().Marshal()) {
		t.Fatal("host key not persistent")
	}
}

func TestLoadOrCreateProjectJumpKey_PersistsPerProject(t *testing.T) {
	dir := t.TempDir()
	_, pubA1, err := LoadOrCreateProjectJumpKey(dir, "projA")
	if err != nil {
		t.Fatal(err)
	}
	_, pubA2, err := LoadOrCreateProjectJumpKey(dir, "projA")
	if err != nil {
		t.Fatal(err)
	}
	_, pubB, err := LoadOrCreateProjectJumpKey(dir, "projB")
	if err != nil {
		t.Fatal(err)
	}
	if string(pubA1.Marshal()) != string(pubA2.Marshal()) {
		t.Fatal("projA key not persistent")
	}
	if string(pubA1.Marshal()) == string(pubB.Marshal()) {
		t.Fatal("projA and projB keys must differ")
	}
}

// testGenHostKey returns a fresh ed25519 ssh.Signer for test use.
func testGenHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// testGenClientKey returns an OpenSSH-pem priv blob and the matching ssh.PublicKey.
func testGenClientKey(t *testing.T) ([]byte, ssh.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pemBytes, err := ssh.MarshalPrivateKey(priv, "test")
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(pemBytes)
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return encoded, sshPub
}
