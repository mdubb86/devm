// Package schema — scripts: field parsing and validation.
//
// A named script is a list of shell commands the engine joins with " && "
// and runs under one `bash -eo pipefail -c` at install: time (or emits
// inline into startup.sh at startup: time). References are strings in
// install:/startup: whose first non-whitespace character is `>`.
package schema

import (
	"fmt"
	"regexp"
	"strings"
)

// scriptNameRE is the required shape of a script name: kebab-case,
// starting with a lowercase letter. Enforced at config load.
var scriptNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ParseScriptRef inspects a single install:/startup: entry. Returns
// (name, true) when s's first non-whitespace character is `>`; the
// returned name is the whitespace-trimmed remainder (may be empty —
// callers should feed it through ValidateScriptName). Returns ("",
// false) for plain shell commands.
//
// The parser is intentionally lenient about whitespace: `>foo`,
// `> foo`, and `  >  foo  ` all resolve to `foo`. This keeps the
// YAML source flexible without introducing ambiguity with real shell
// commands, which never start with `>` in this codebase's install:
// discipline (redirection to a file always appears mid-command).
func ParseScriptRef(s string) (name string, isRef bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, ">") {
		return "", false
	}
	return strings.TrimSpace(trimmed[1:]), true
}

// ValidateScriptName returns nil if name matches [a-z][a-z0-9-]* and
// an error explaining the shape rule otherwise.
func ValidateScriptName(name string) error {
	if !scriptNameRE.MatchString(name) {
		return fmt.Errorf("script name %q must match [a-z][a-z0-9-]* (kebab-case, starts with a letter)", name)
	}
	return nil
}
