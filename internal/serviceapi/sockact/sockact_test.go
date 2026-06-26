package sockact

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestActivate_OutsideLaunchd verifies the graceful failure path:
// when the process wasn't started by launchd (typical for `go test`),
// Activate must return ErrNotActivated rather than panicking or
// returning a generic error. Callers rely on this distinction.
func TestActivate_OutsideLaunchd(t *testing.T) {
	_, err := Activate("HTTPSocket")
	assert.ErrorIs(t, err, ErrNotActivated)
}
