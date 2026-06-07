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
