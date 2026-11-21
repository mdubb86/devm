package render

import "fmt"

// SSHAgentBridgeUnit renders the systemd unit for the guest-side
// ssh-agent bridge. Enabled by install.sh when the project's `ssh:`
// list is non-empty.
func SSHAgentBridgeUnit(macHost string, macPort int, sockPath string) []byte {
	return []byte(fmt.Sprintf(`[Unit]
Description=devm guest-side SSH agent bridge
After=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/gdevm ssh-agent-bridge --mac-host=%s --mac-port=%d --sock=%s
Restart=on-failure
RestartSec=1
User=devm
Group=devm
RuntimeDirectory=devm

[Install]
WantedBy=multi-user.target
`, macHost, macPort, sockPath))
}
