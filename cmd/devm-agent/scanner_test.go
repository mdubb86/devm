package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeProc writes /proc/<pid>/comm and /proc/<pid>/stat under root.
// stat has the canonical layout; tty_nr (field 7) is the only field we
// care about, but we fill realistic placeholders elsewhere.
func writeFakeProc(t *testing.T, root, pid, comm string, ttyNr int) {
	t.Helper()
	pidDir := filepath.Join(root, pid)
	require.NoError(t, os.MkdirAll(pidDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pidDir, "comm"), []byte(comm+"\n"), 0o644))
	// pid (comm) state ppid pgrp session tty_nr tpgid ...
	stat := pid + " (" + comm + ") S 1 1 1 " + itoaStat(ttyNr) + " -1 0 0 0 0 0 0 0 0 20 0 1 0 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(pidDir, "stat"), []byte(stat), 0o644))
}

func itoaStat(n int) string {
	// Local helper so we don't depend on strconv here.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestProcScannerEmpty(t *testing.T) {
	root := t.TempDir()
	s := NewProcScanner(root)
	assert.Equal(t, 0, s.Sessions())
}

func TestProcScannerFindsShell(t *testing.T) {
	root := t.TempDir()
	writeFakeProc(t, root, "100", "bash", 34816) // arbitrary non-zero tty_nr
	s := NewProcScanner(root)
	assert.Equal(t, 1, s.Sessions())
}

func TestProcScannerIgnoresNoTTY(t *testing.T) {
	root := t.TempDir()
	writeFakeProc(t, root, "100", "bash", 0)
	s := NewProcScanner(root)
	assert.Equal(t, 0, s.Sessions())
}

func TestProcScannerIgnoresUnknownName(t *testing.T) {
	root := t.TempDir()
	writeFakeProc(t, root, "100", "nginx", 34816)
	s := NewProcScanner(root)
	assert.Equal(t, 0, s.Sessions())
}

func TestProcScannerCountsMultiple(t *testing.T) {
	root := t.TempDir()
	writeFakeProc(t, root, "100", "bash", 34816)
	writeFakeProc(t, root, "101", "zsh", 34817)
	writeFakeProc(t, root, "102", "nginx", 34818) // ignored
	writeFakeProc(t, root, "103", "bash", 0)      // ignored (no tty)
	s := NewProcScanner(root)
	assert.Equal(t, 2, s.Sessions())
}

func TestProcScannerIgnoresNonPIDDirs(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "self"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "self", "comm"), []byte("bash\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "self", "stat"), []byte("0 (bash) S 1 1 1 34816 -1\n"), 0o644))
	s := NewProcScanner(root)
	assert.Equal(t, 0, s.Sessions())
}

func TestTTYFromStatHandlesParensInComm(t *testing.T) {
	// comm can contain spaces and parens.
	line := "100 (weird (proc) name) S 1 1 1 34816 -1 0 0 0 0 0 0 0 0 20 0 1 0 0\n"
	tty, ok := ttyFromStat(line)
	require.True(t, ok)
	assert.Equal(t, 34816, tty)
}
