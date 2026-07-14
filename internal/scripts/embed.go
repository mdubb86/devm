package scripts

import _ "embed"

//go:embed install-templates.sh
var InstallTemplates string

//go:embed with-devm-env.sh
var WithDevmEnv string

//go:embed install.sh
var Install string
