package scripts

import _ "embed"

//go:embed provision.sh
var Provision string

//go:embed init-volumes.sh
var InitVolumes string

//go:embed devm-exec.sh
var DevmExec string
