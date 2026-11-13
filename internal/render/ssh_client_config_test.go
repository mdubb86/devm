package render

import (
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/schema"
)

func TestSSHClientConfig_EmitsJumpBlockAndTargetBlock(t *testing.T) {
	body := SSHClientConfig(55123, "/run/devm/ssh-agent.sock", []schema.ResolvedSSHTarget{
		{Alias: "prod", HostName: "ops.example.com", Port: 22, User: "deploy"},
	})
	s := string(body)
	for _, want := range []string{
		"Host devm-jump",
		"HostName 192.168.127.254",
		"Port 55123",
		"User devm",
		"IdentityFile ~/.ssh/devm-jump-id",
		"IdentitiesOnly yes",
		"PasswordAuthentication no",
		"Host prod",
		"HostName ops.example.com",
		"Port 22",
		"User deploy",
		"ProxyJump devm-jump",
		"IdentityAgent /run/devm/ssh-agent.sock",
		"PreferredAuthentications publickey",
		"KbdInteractiveAuthentication no",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "HostKeyAlias") {
		t.Errorf("empty HostKeyAlias must not emit line:\n%s", s)
	}
}

func TestSSHClientConfig_EmitsOneBlockPerTarget(t *testing.T) {
	body := SSHClientConfig(55123, "/run/devm/ssh-agent.sock", []schema.ResolvedSSHTarget{
		{Alias: "a", HostName: "a.net", Port: 22, User: "u"},
		{Alias: "b", HostName: "b.net", Port: 2222, User: "v"},
	})
	s := string(body)
	if strings.Count(s, "ProxyJump devm-jump") != 2 {
		t.Fatalf("want 2 ProxyJump lines:\n%s", s)
	}
}

func TestSSHClientConfig_EmitsHostKeyAlias(t *testing.T) {
	body := SSHClientConfig(55123, "/run/devm/ssh-agent.sock", []schema.ResolvedSSHTarget{
		{Alias: "foo", HostName: "127.0.0.1", Port: 22, User: "me", HostKeyAlias: "devm-foo"},
	})
	s := string(body)
	if !strings.Contains(s, "HostKeyAlias devm-foo") {
		t.Fatalf("HostKeyAlias line missing:\n%s", s)
	}
}
