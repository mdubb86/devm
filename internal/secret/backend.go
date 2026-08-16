// Package secret stores per-project secrets as mode-0600 files under
// identity.Config.SecretsDir() (~/Library/Application Support/devm/secrets/
// for prod). The Backend interface lets tests use an in-memory fake;
// production uses NewFileBackend. NewMacKeychain remains for the
// one-time migration script that reads the old macOS-keychain items
// and writes them into the file store.
//
// Security posture: files are 0600 under a 0700 root. On a FileVault-
// enabled Mac the store is encrypted at rest; macOS TCC also gates
// programmatic access under ~/Library/. Vs. the prior keychain
// backend the store loses per-secret ACL prompts — but keychain
// prompts every rebuild anyway under ad-hoc code signing, which for
// a many-secrets workload made the tool unusable.
package secret

import "errors"

// ServiceName is the kSecAttrService value the legacy macOS keychain
// backend used for every devm secret. Preserved because the migration
// script still reads keychain items to import them into the file
// backend.
const ServiceName = "devm"

// ErrNotFound is returned by Backend.Get and Backend.Delete when the
// account doesn't exist in the keychain.
var ErrNotFound = errors.New("secret not found")

// Backend is the interface satisfied by both the real keychain and
// the in-memory fake.
type Backend interface {
	// Set stores `value` at the given account name. Overwrites any
	// existing entry at that account.
	Set(account, value string) error

	// Get returns the value at the given account, or ErrNotFound.
	Get(account string) (string, error)

	// List returns just the leaf names (after the project prefix)
	// of every account starting with `<projectID>/`. Order
	// unspecified.
	List(projectID string) ([]string, error)

	// Delete removes the account. ErrNotFound if absent.
	Delete(account string) error
}

// NewMacKeychain returns a Backend backed by the macOS login
// keychain. Kept only for the one-time migration script that reads
// existing keychain items and writes them into NewFileBackend's
// on-disk store. No production callsite uses it directly. On
// non-darwin builds every method returns an "unsupported on this
// platform" error — the constructor itself never fails, so CI
// vet/test on Linux compiles cleanly.
func NewMacKeychain() Backend { return &macKeychain{} }

type macKeychain struct{}
