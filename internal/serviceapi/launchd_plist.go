package serviceapi

// LaunchdPlistTemplate is the plist text that kardianos uses when we
// pass it as service.KeyValue{"LaunchdConfig": LaunchdPlistTemplate}.
// The {{.Name}} and {{.Path}} placeholders are substituted by
// kardianos's template engine.
//
// Why custom: kardianos's default plist doesn't set UserName,
// EnvironmentVariables, or explicit log paths, all of which the daemon
// needs. No Sockets dict — since B3 (per-project bind isolation), all
// daemon-proxy binds (:80/:443 per project IP) come from the
// helper (internal/helper), not launchd socket handoff.
const LaunchdPlistTemplate = `<?xml version='1.0' encoding='UTF-8'?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN"
"http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version='1.0'>
<dict>
    <key>Label</key>
    <string>{{html .Name}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{html .Path}}</string>
    {{range .Config.Arguments}}
        <string>{{html .}}</string>
    {{end}}
    </array>
    <key>KeepAlive</key>
    <true/>
    <key>RunAtLoad</key>
    <true/>
    <key>UserName</key>
    <string>__USER__</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>__HOME__</string>
        <key>USER</key>
        <string>__USER__</string>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>
    <key>StandardOutPath</key>
    <string>__LOG_OUT__</string>
    <key>StandardErrorPath</key>
    <string>__LOG_ERR__</string>
</dict>
</plist>
`
