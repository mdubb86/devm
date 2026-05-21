package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Scanner reports the number of active user-session processes currently
// running inside the sandbox. Implementations decide what counts as a
// session; the agent treats the count as opaque.
type Scanner interface {
	Sessions() int
}

// sessionProcNames is the whitelist of process names that indicate a user
// session. Anything else is ignored. Refine this once we observe what sbx
// actually produces during interactive sessions.
var sessionProcNames = map[string]struct{}{
	"bash":   {},
	"zsh":    {},
	"fish":   {},
	"sh":     {},
	"claude": {},
	"vim":    {},
	"nvim":   {},
	"devm":   {},
	"screen": {},
	"tmux":   {},
}

// ProcScanner walks a /proc-style filesystem rooted at procRoot and counts
// processes whose comm is in sessionProcNames AND which have a controlling
// terminal (tty_nr != 0 in /proc/<pid>/stat).
type ProcScanner struct {
	procRoot string
}

// NewProcScanner returns a scanner reading from procRoot. Pass "/proc" in
// production; tests pass a t.TempDir() populated with a fake layout.
func NewProcScanner(procRoot string) *ProcScanner {
	return &ProcScanner{procRoot: procRoot}
}

// Sessions returns the count of user-session processes. Errors reading
// individual PID directories are skipped (a process can disappear between
// our enumerating and reading it).
func (s *ProcScanner) Sessions() int {
	entries, err := os.ReadDir(s.procRoot)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue // not a PID dir
		}
		if isSessionProc(filepath.Join(s.procRoot, e.Name())) {
			count++
		}
	}
	return count
}

// isSessionProc returns true if the process at procDir has a whitelisted
// comm and a non-zero controlling tty.
func isSessionProc(procDir string) bool {
	commBytes, err := os.ReadFile(filepath.Join(procDir, "comm"))
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(commBytes))
	if _, ok := sessionProcNames[comm]; !ok {
		return false
	}
	statBytes, err := os.ReadFile(filepath.Join(procDir, "stat"))
	if err != nil {
		return false
	}
	tty, ok := ttyFromStat(string(statBytes))
	if !ok {
		return false
	}
	return tty != 0
}

// ttyFromStat parses tty_nr (field 7 of /proc/<pid>/stat). The comm field
// (field 2) is parenthesized and may contain spaces and parens, so we
// split on the LAST ')' and parse subsequent space-separated fields.
func ttyFromStat(stat string) (int, bool) {
	last := strings.LastIndex(stat, ")")
	if last < 0 || last+1 >= len(stat) {
		return 0, false
	}
	fields := strings.Fields(stat[last+1:])
	// After comm, fields are: state ppid pgrp session tty_nr ...
	// Index 4 of the post-comm slice is tty_nr.
	if len(fields) < 5 {
		return 0, false
	}
	v, err := strconv.Atoi(fields[4])
	if err != nil {
		return 0, false
	}
	return v, true
}
