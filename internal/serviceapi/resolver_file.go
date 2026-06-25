package serviceapi

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ResolverFilePath is where macOS reads per-TLD forwarding rules
// for *.test. Owned by root; reading is unprivileged but writing
// requires sudo.
const ResolverFilePath = "/etc/resolver/test"

// ResolverFileState classifies what's currently at ResolverFilePath.
type ResolverFileState int

const (
	ResolverFileMissing  ResolverFileState = iota // file doesn't exist
	ResolverFileMatches                           // file equals canonical
	ResolverFileDiverged                          // file exists but differs
)

// canonicalResolverContents is exactly what we write. Bytes matter:
// CheckResolverFile uses byte-equality.
func canonicalResolverContents() string {
	return "nameserver 127.0.0.1\nport 51153\n"
}

// CheckResolverFile reads ResolverFilePath (no sudo needed) and
// reports its state.
func CheckResolverFile() (ResolverFileState, error) {
	return checkResolverFileAt(ResolverFilePath)
}

func checkResolverFileAt(path string) (ResolverFileState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ResolverFileMissing, nil
		}
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	if string(data) == canonicalResolverContents() {
		return ResolverFileMatches, nil
	}
	return ResolverFileDiverged, nil
}

// WriteResolverFile shells out to `sudo mkdir -p /etc/resolver` then
// `sudo tee /etc/resolver/test` to install the canonical contents.
// Inherits the user's TTY so sudo can prompt for the password.
// Returns an error if sudo fails (user cancels, wrong password,
// sudo not on PATH).
//
// Why mkdir: /etc/resolver/ doesn't exist by default on macOS; the
// first tool to use this mechanism creates it.
func WriteResolverFile() error {
	mkdir := exec.Command("sudo", "mkdir", "-p", "/etc/resolver")
	mkdir.Stdin = os.Stdin
	mkdir.Stdout = os.Stdout
	mkdir.Stderr = os.Stderr
	if err := mkdir.Run(); err != nil {
		return fmt.Errorf("sudo mkdir /etc/resolver: %w", err)
	}

	tee := exec.Command("sudo", "tee", ResolverFilePath)
	tee.Stdin = strings.NewReader(canonicalResolverContents())
	tee.Stdout = io.Discard
	tee.Stderr = os.Stderr
	if err := tee.Run(); err != nil {
		return fmt.Errorf("sudo tee %s: %w", ResolverFilePath, err)
	}
	return nil
}

// RemoveResolverFile shells out to `sudo rm` to remove
// ResolverFilePath. Same TTY-inherit pattern as Write.
func RemoveResolverFile() error {
	cmd := exec.Command("sudo", "rm", ResolverFilePath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo rm %s: %w", ResolverFilePath, err)
	}
	return nil
}
