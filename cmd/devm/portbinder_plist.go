package main

import "fmt"

// portbinderPlistContent renders the com.devm.portbinder LaunchDaemon
// plist. The helper runs as root (needed to bind privileged loopback
// aliases on behalf of unprivileged project VMs — see
// internal/portbinder), so it's installed as a system LaunchDaemon
// alongside the main devm service, not embedded in its Sockets block
// (Task 4 dropped that).
func portbinderPlistContent() string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.devm.portbinder</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/var/log/devm-portbinder.log</string>
    <key>StandardErrorPath</key>
    <string>/var/log/devm-portbinder.log</string>
</dict>
</plist>
`, portbinderInstallPath)
}
