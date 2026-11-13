package render

import (
	"strings"
	"testing"
)

func TestSSHAgentBridgeUnit_HasKeyDirectives(t *testing.T) {
	body := SSHAgentBridgeUnit("192.168.127.254", 55124, "/run/devm/ssh-agent.sock")
	s := string(body)
	for _, want := range []string{
		"[Service]",
		"ExecStart=/usr/local/bin/gdevm ssh-agent-bridge",
		"--mac-host=192.168.127.254",
		"--mac-port=55124",
		"--sock=/run/devm/ssh-agent.sock",
		"Restart=on-failure",
		"User=devm",
		"Group=devm",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
}
