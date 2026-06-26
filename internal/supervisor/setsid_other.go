//go:build !darwin

package supervisor

import "os/exec"

// applySetsid is a no-op on non-darwin. devm is macOS-only; this stub
// just lets the package compile during `go vet` / cross-platform CI.
func applySetsid(*exec.Cmd) {}
