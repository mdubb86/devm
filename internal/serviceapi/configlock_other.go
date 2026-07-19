//go:build !darwin

package serviceapi

// setImmutable is a no-op on non-darwin. UF_IMMUTABLE/chflags is a BSD
// facility devm only needs on macOS; this stub just lets the package
// build and vet during cross-platform CI.
func setImmutable(path string, want bool) error { return nil }
