package serviceapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestInstallCATrust_FailsWithoutSudo: even when sudo is cached,
// the cert path doesn't exist in this test environment, so we expect
// some error.
func TestInstallCATrust_FailsWithoutSudo(t *testing.T) {
	err := InstallCATrust("/tmp/nonexistent-devm-test-cert.pem")
	assert.Error(t, err)
}

// TestUninstallCATrust_DoesNotPanic: removing a cert that isn't
// there may error or succeed depending on environment — we just
// want it to NOT panic.
func TestUninstallCATrust_DoesNotPanic(t *testing.T) {
	_ = UninstallCATrust()
}
