// Package scriptfile parses devm.sh (and devm.me.sh) — the project's
// bash function library — enumerates the function names declared at
// the top level, and renders the invocation wrapper the daemon uses to
// call one at install/startup/command/service time.
package scriptfile

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// FunctionNameRE is the required shape of a callable function name:
// kebab-case, starts with a lowercase letter.
var FunctionNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ValidateFunctionName returns nil if name matches FunctionNameRE.
func ValidateFunctionName(name string) error {
	if !FunctionNameRE.MatchString(name) {
		return fmt.Errorf("function name %q must match [a-z][a-z0-9-]* (kebab-case, starts with a letter)", name)
	}
	return nil
}

// Parse enumerates top-level function names declared in a bash script.
// Recognises two forms:
//
//	name() { ... }
//	function name { ... }        (with or without parens)
//
// A function definition is "top-level" when its opening `{` occurs at
// brace depth zero. Definitions nested inside other functions are
// skipped: not addressable from devm.yaml, so ignoring them is safe.
//
// Not a full bash parser. Reads line-by-line, tracks brace depth by
// counting `{`/`}` outside quotes, skips comments and heredoc bodies.
// Good enough for the shape users actually write in devm.sh; anything
// exotic (e.g. eval-generated function defs) simply isn't enumerable
// and would surface at run time.
func Parse(body []byte) ([]string, error) {
	var out []string
	depth := 0
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := stripComment(sc.Text())
		if depth == 0 {
			if name, ok := declLineName(line); ok {
				out = append(out, name)
			}
		}
		depth += braceDelta(line)
		if depth < 0 {
			depth = 0
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan devm.sh: %w", err)
	}
	return out, nil
}

// declLineName inspects a line for a top-level function declaration and
// returns the declared name. Handles `name()` and `function name`.
func declLineName(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if s == "" {
		return "", false
	}
	if after, ok := strings.CutPrefix(s, "function "); ok {
		rest := strings.TrimSpace(after)
		name := rest
		if i := strings.IndexAny(rest, " (\t{"); i >= 0 {
			name = rest[:i]
		}
		if FunctionNameRE.MatchString(name) {
			return name, true
		}
		return "", false
	}
	// `name() { ...` — accept even if `{` is on a following line.
	if i := strings.Index(s, "()"); i > 0 {
		name := strings.TrimSpace(s[:i])
		if FunctionNameRE.MatchString(name) {
			return name, true
		}
	}
	return "", false
}

// braceDelta counts unquoted `{` and `}` on the line.
func braceDelta(line string) int {
	d := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '"' && (i == 0 || line[i-1] != '\\') {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '{':
			d++
		case c == '}':
			d--
		}
	}
	return d
}

// stripComment removes a trailing `# ...` comment from a line, honoring
// simple single/double quoting so `echo "# not a comment"` stays intact.
func stripComment(line string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '"' && (i == 0 || line[i-1] != '\\') {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '#':
			return line[:i]
		}
	}
	return line
}
