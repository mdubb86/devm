package serviceapi

import (
	"errors"
	"fmt"
	"os"
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

// CanonicalResolverContents is exactly what we write. Bytes matter:
// CheckResolverFile uses byte-equality. Exported so cmd/devm/service.go
// can include it in the consolidated install shell script.
func CanonicalResolverContents() string {
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
	if string(data) == CanonicalResolverContents() {
		return ResolverFileMatches, nil
	}
	return ResolverFileDiverged, nil
}
