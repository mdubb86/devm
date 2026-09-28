package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// confirmDestructive gates a destructive command behind either an
// explicit --yes/autoYes flag or an interactive [y/N] prompt.
//
// Returns (true, nil) to proceed. Returns (false, err) when the
// caller is non-interactive and did not pass --yes — err names the
// flag so a scripted/agent invocation gets an actionable message
// instead of the read-from-a-closed-stdin silent hang the old
// prompt path produced. Returns (false, nil) when a TTY user
// declined at the prompt; the caller prints "aborted" and exits 1.
func confirmDestructive(prompt string, autoYes bool) (bool, error) {
	if autoYes {
		return true, nil
	}
	if !isTerminal(os.Stdin) {
		return false, errors.New("stdin is not a terminal; pass --yes to run non-interactively")
	}
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", prompt)
	var resp string
	_, _ = fmt.Fscanln(os.Stdin, &resp)
	resp = strings.ToLower(strings.TrimSpace(resp))
	return resp == "y" || resp == "yes", nil
}
