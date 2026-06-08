package scripts

import _ "embed"

//go:embed init-volumes.sh
var InitVolumes string

//go:embed devm-exec.sh
var DevmExec string

//go:embed install-templates.sh
var InstallTemplates string

//go:embed bootstrap.sh
var Bootstrap string

//go:embed with-devm-env.sh
var WithDevmEnv string

//go:embed wrap-fg.sh
var WrapFG string

//go:embed wrap-bg.sh
var WrapBG string

// s6-log binaries: extracted from s6-overlay v3.2.0.2 (ISC licensed).
// Used by wrap-bg.sh for rotated capture of background daemon output.
// See THIRD_PARTY_NOTICES.md and internal/render/write.go for the
// ISC attribution shipped alongside the binary at install time.
//
//go:embed s6-log.linux-arm64
var S6LogLinuxARM64 []byte

//go:embed s6-log.linux-amd64
var S6LogLinuxAMD64 []byte
