package orchestrator

import (
	"strings"
)

// shortenForSpinner collapses a possibly-multiline shell command into
// a single-line label that pterm's spinner can render without
// redrawing many lines per tick. Strips backslash-newline
// continuations, replaces remaining newlines with spaces, collapses
// whitespace runs, and truncates with an ellipsis if longer than max.
func shortenForSpinner(desc string, max int) string {
	desc = strings.ReplaceAll(desc, "\\\n", " ")
	desc = strings.ReplaceAll(desc, "\n", " ")
	desc = strings.Join(strings.Fields(desc), " ")
	if max > 0 && len(desc) > max {
		// "…" is 3 bytes in UTF-8 — reserve room so the final
		// byte length stays at or below max.
		desc = desc[:max-3] + "…"
	}
	return desc
}

// isCountedStep reports whether step N of phase is a user-defined step
// (counted toward [K/Total]) or devm-internal (uncounted).
//
//	install:   step 1     = bootstrap (uncounted)
//	           step 2..N+1 = user (counted)
//	startup:   step 0 = cleanup (not displayed)
//	           step 1 = devm-startup (uncounted)
//	           step 2 = install-templates (uncounted)
//	           step 3..M+2 = user (counted)
func isCountedStep(phase string, stepN int) bool {
	if phase == "install" {
		return stepN >= 2
	}
	if phase == "startup" {
		return stepN >= 3
	}
	return false
}
