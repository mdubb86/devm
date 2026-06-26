package serviceapi

// LaunchdPlistTemplate is the plist text that kardianos uses when we
// pass it as service.KeyValue{"LaunchdConfig": LaunchdPlistTemplate}.
// The {{.Name}} and {{.Path}} placeholders are substituted by
// kardianos's template engine.
//
// Why custom: kardianos's default plist doesn't include a Sockets
// dict. We need that for Ship 3 — launchd pre-binds :80 and :443 as
// root and hands the file descriptors to our user-level service via
// launch_activate_socket.
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
    <key>StandardOutPath</key>
    <string>__LOG_OUT__</string>
    <key>StandardErrorPath</key>
    <string>__LOG_ERR__</string>
    <key>Sockets</key>
    <dict>
        <key>HTTPSocket</key>
        <dict>
            <key>SockNodeName</key>
            <string>0.0.0.0</string>
            <key>SockServiceName</key>
            <string>80</string>
            <key>SockType</key>
            <string>stream</string>
        </dict>
        <key>HTTPSSocket</key>
        <dict>
            <key>SockNodeName</key>
            <string>0.0.0.0</string>
            <key>SockServiceName</key>
            <string>443</string>
            <key>SockType</key>
            <string>stream</string>
        </dict>
    </dict>
</dict>
</plist>
`
