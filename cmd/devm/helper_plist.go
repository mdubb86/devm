package main

import (
	"fmt"
	"path/filepath"
)

// helperPlistContent renders the helper's LaunchDaemon plist.
// programPath is the resolved devm-helper binary (sibling of the devm
// CLI binary — no system-path copy) and logDir is the installing
// user's ~/Library/Logs. The helper runs as root (needed to bind
// privileged loopback aliases on behalf of unprivileged project VMs
// — see internal/helper), so it's installed as a system LaunchDaemon
// alongside the main devm service, not embedded in its Sockets block.
//
// Every identity-derived value (Label, GroupName, log paths) comes
// from the package cfg var so prod and e2e installs never collide:
// distinct labels, distinct log files, distinct socket-gating group.
func helperPlistContent(programPath, logDir string) string {
	label := cfg.LaunchdLabelHelper()
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>GroupName</key>
    <string>%s</string>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, label, programPath, cfg.GroupName(),
		filepath.Join(logDir, label+".out.log"),
		filepath.Join(logDir, label+".err.log"))
}
