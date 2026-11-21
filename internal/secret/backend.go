// Package secret stores per-project secrets as mode-0600 files under
// identity.Config.SecretsDir() (~/Library/Application Support/devm/secrets/
// for prod). The Backend interface lets tests use an in-memory fake;
// production uses NewFileBackend.
//
// Security posture: files are 0600 under a 0700 root. On a FileVault-
// enabled Mac the store is encrypted at rest; macOS TCC also gates
// programmatic access under ~/Library/. The macOS keychain is
// deliberately not used: devm ships ad-hoc-signed by policy, and
// keychain ACLs key on the binary's cdhash, so every rebuild would
// re-prompt per secret — unusable for a many-secrets workload. The
// trade-off is losing per-secret ACL prompts, a threat model where a
// hostile process already runs in the user's session.
package secret

import "errors"

// ErrNotFound is returned by Backend.Get and Backend.Delete when the
// account doesn't exist in the store.
var ErrNotFound = errors.New("secret not found")

// Backend is the interface satisfied by the file store and the
// in-memory fake.
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
