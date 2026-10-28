//go:build !darwin

package serviceapi

import "fmt"

// listenersBoundTo stub for non-darwin builds. The devm daemon is
// macOS-only; this file exists so `go vet` / `go build` succeed on
// other platforms in CI. The stub is never called in practice.
var listenersBoundTo = func(string) ([]int, error) {
	return nil, fmt.Errorf("listener query not supported on this GOOS")
}
