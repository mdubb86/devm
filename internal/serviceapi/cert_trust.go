package serviceapi

import (
	"fmt"
	"os/exec"
)

// Constants exported so cmd/devm/service.go can build the
// consolidated install/uninstall shell scripts. The sudo-bearing
// `security add-trusted-cert` / `delete-certificate` invocations
// live in cmd/devm/service.go directly so they share a single sudo
// session with the DNS resolver setup.
const (
	CATrustCertCN  = "devm Local CA"
	SystemKeychain = "/Library/Keychains/System.keychain"
)

// CheckCATrusted returns true if a cert with our CN is present in
// the System Keychain. False with nil err means "not present"; a
// non-nil err means the `security` command failed.
//
// Reading the System Keychain doesn't require sudo — it's
// world-readable.
func CheckCATrusted() (bool, error) {
	cmd := exec.Command("security", "find-certificate",
		"-c", CATrustCertCN, SystemKeychain)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	// Exit code 44 (kSecItemNotFound) is the normal "not present"
	// case; treat as a clean false.
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
		return false, nil
	}
	return false, fmt.Errorf("security find-certificate: %w (output: %s)",
		err, string(out))
}
