package render

import (
	"fmt"

	"github.com/mtwaage/devm/internal/schema"
	"gopkg.in/yaml.v3"
)

// LintRenderedSpec renders the spec.yaml for cfg and parses it back as
// generic YAML. Catches render bugs (unquoted YAML-reserved scalars,
// indentation glitches, etc.) before they reach sbx — both at write
// time (writeStaticFiles) and from `devm validate` (without needing to
// actually shell into the sandbox).
//
// Why we need this: devm.yaml schema validation only checks the SOURCE
// config. The rendered spec.yaml is built with raw string templating
// in render/spec.go, and any user-controlled bare scalar (allowed
// domains, identifiers, etc.) can produce invalid YAML if not quoted.
// Round-tripping the output through the yaml parser catches the whole
// class in one place.
func LintRenderedSpec(cfg schema.Config, repoRoot string) error {
	rendered := SpecYAML(cfg, repoRoot)
	var sink any
	if err := yaml.Unmarshal([]byte(rendered), &sink); err != nil {
		return fmt.Errorf("rendered .devm/spec.yaml is not valid YAML: %w", err)
	}
	return nil
}
