package serviceapi

import (
	"fmt"
	"os"
	"os/exec"
)

const (
	caTrustCertCN  = "devm Local CA"
	systemKeychain = "/Library/Keychains/System.keychain"
)

// CheckCATrusted returns true if a cert with our CN is present in
// the System Keychain. False with nil err means "not present"; a
// non-nil err means the `security` command failed.
//
// Reading the System Keychain doesn't require sudo — it's
// world-readable.
func CheckCATrusted() (bool, error) {
	cmd := exec.Command("security", "find-certificate",
		"-c", caTrustCertCN, systemKeychain)
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

// InstallCATrust shells out to `sudo security add-trusted-cert ...`
// to install the CA root into the System Keychain as a trusted root
// for TLS server auth. Inherits the user's TTY so sudo can prompt
// for password.
//
// rootCertPath is the on-disk PEM file (typically
// ~/Library/Application Support/devm/ca/root.crt).
func InstallCATrust(rootCertPath string) error {
	cmd := exec.Command("sudo", "security", "add-trusted-cert",
		"-d", "-r", "trustRoot",
		"-k", systemKeychain,
		rootCertPath,
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo security add-trusted-cert: %w", err)
	}
	return nil
}

// UninstallCATrust removes the CA root from the System Keychain.
func UninstallCATrust() error {
	cmd := exec.Command("sudo", "security", "delete-certificate",
		"-c", caTrustCertCN,
		"-t",
		systemKeychain,
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo security delete-certificate: %w", err)
	}
	return nil
}
