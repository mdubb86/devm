// Package secret stores per-project secrets in the macOS login keychain.
//
// The Backend interface lets tests use an in-memory fake. Production
// uses NewMacKeychain which wraps github.com/keybase/go-keychain
// (darwin-only — non-darwin builds return errors from every method).
package secret

import "errors"

// ServiceName is the kSecAttrService value all devm secrets share.
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

// NewMacKeychain returns the production Backend backed by the macOS
// login keychain. On non-darwin builds every method returns an
// "unsupported on this platform" error — the constructor itself
// never fails, so CI vet/test on Linux compiles cleanly.
func NewMacKeychain() Backend { return &macKeychain{} }

type macKeychain struct{}
