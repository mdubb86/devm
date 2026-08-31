package schema

import (
	"fmt"

	shellwords "github.com/mattn/go-shellwords"
)

// ValidateShellCommand refuses shell strings that a POSIX-style word
// splitter cannot tokenize — almost always the fingerprint of an
// unquoted `#` inside a plain YAML scalar. YAML strips ` # …` as a
// comment before devm ever sees the value; the survivor is a command
// ending inside an open `"` or `'`, which shellwords rejects and bash
// would surface a stage later as an unterminated-quote error.
// Refuses early so the author sees which line and why, at config-load
// time, rather than after a provisioning cycle burns on it.
//
// Script refs (`>name`) are the caller's business — this function
// shell-lints literal command strings only.
func ValidateShellCommand(s string) error {
	if _, err := shellwords.Parse(s); err != nil {
		return fmt.Errorf(
			"shell tokenization failed for %q: %w — "+
				"a bare `#` inside an unquoted YAML scalar starts a "+
				"YAML comment; wrap the whole line in single quotes "+
				"('...') to keep `#` as text",
			s, err)
	}
	return nil
}
