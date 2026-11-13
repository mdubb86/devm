package serviceapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/schema"
)

func TestStartSSHJumpListeners_BindsAndIsIdempotent(t *testing.T) {
	runtimeDir := t.TempDir()
	caDir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, caDir)
	if err != nil {
		t.Fatal(err)
	}
	p := NewProxyServer(identity.Prod, NewRoutes(identity.Prod.TLD), ca)

	j1, b1, err := p.StartSSHJumpListeners(context.Background(), "projX", runtimeDir, []schema.ResolvedSSHTarget{
		{Alias: "a", HostName: "127.0.0.1", Port: 22},
	})
	if err != nil {
		t.Fatal(err)
	}
	if j1 == 0 || b1 == 0 {
		t.Fatalf("ports zero: %d %d", j1, b1)
	}

	j2, b2, err := p.StartSSHJumpListeners(context.Background(), "projX", runtimeDir, []schema.ResolvedSSHTarget{
		{Alias: "a", HostName: "127.0.0.1", Port: 22},
	})
	if err != nil {
		t.Fatal(err)
	}
	if j1 != j2 || b1 != b2 {
		t.Fatalf("idempotent bind changed ports: (%d,%d) → (%d,%d)", j1, b1, j2, b2)
	}
	p.StopSSHJumpListeners("projX")
}

func TestStartSSHJumpListeners_AllowsConnectionWithProjectKey(t *testing.T) {
	runtimeDir := t.TempDir()
	caDir := t.TempDir()
	ca, _ := loadOrGenerateCAAt(identity.Prod, caDir)
	p := NewProxyServer(identity.Prod, NewRoutes(identity.Prod.TLD), ca)

	// Also start a fake target the jump will allowlist.
	tgt, _ := net.Listen("tcp", "127.0.0.1:0")
	defer tgt.Close()
	tgtPort := tgt.Addr().(*net.TCPAddr).Port

	jumpPort, _, err := p.StartSSHJumpListeners(context.Background(), "projX", runtimeDir, []schema.ResolvedSSHTarget{
		{Alias: "target", HostName: "127.0.0.1", Port: tgtPort},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.StopSSHJumpListeners("projX")

	// Load the per-project client key that Start just created.
	privBytes, _, err := LoadOrCreateProjectJumpKey(runtimeDir, "projX")
	if err != nil {
		t.Fatal(err)
	}
	_ = privBytes // keep the signer path in sync if we change encoding
	signer, err := ssh.ParsePrivateKey(privBytes)
	if err != nil {
		t.Fatal(err)
	}

	// And the matching host key.
	hostSigner, err := LoadOrCreateJumpHostKey(runtimeDir)
	if err != nil {
		t.Fatal(err)
	}

	client, err := ssh.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(jumpPort)), &ssh.ClientConfig{
		User:            "devm",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostSigner.PublicKey()),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	conn, err := client.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(tgtPort)))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

// Suppress unused imports in alternative builds.
var (
	_ = ed25519.PublicKey{}
	_ = rand.Reader
	_ = pem.EncodeToMemory
)
