//go:build !darwin

package serviceapi

// setImmutable is a no-op on non-darwin. UF_IMMUTABLE/chflags is a BSD
// facility devm only needs on macOS; this stub just lets the package
// build and vet during cross-platform CI.
func setImmutable(path string, want bool) error { return nil }

// fileIsImmutable is always false on non-darwin — matches setImmutable's
// no-op stance so the escape-hatch branch never spuriously reports a
// file "was locked" on a platform where nothing sets the flag.
func fileIsImmutable(path string) (bool, error) { return false, nil }
