// Package secret stores per-project secrets in the macOS login keychain.
//
// The Backend interface lets tests use an in-memory fake. Production
// uses NewMacKeychain which wraps github.com/keybase/go-keychain.
package secret

import (
	"errors"

	"github.com/keybase/go-keychain"
)

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
// login keychain.
func NewMacKeychain() Backend { return &macKeychain{} }

type macKeychain struct{}

func (m *macKeychain) Set(account, value string) error {
	item := keychain.NewItem()
	item.SetSecClass(keychain.SecClassGenericPassword)
	item.SetService(ServiceName)
	item.SetAccount(account)
	item.SetData([]byte(value))
	item.SetSynchronizable(keychain.SynchronizableNo)
	item.SetAccessible(keychain.AccessibleWhenUnlocked)

	// Overwrite if present.
	_ = m.Delete(account)
	return keychain.AddItem(item)
}

func (m *macKeychain) Get(account string) (string, error) {
	q := keychain.NewItem()
	q.SetSecClass(keychain.SecClassGenericPassword)
	q.SetService(ServiceName)
	q.SetAccount(account)
	q.SetMatchLimit(keychain.MatchLimitOne)
	q.SetReturnData(true)

	results, err := keychain.QueryItem(q)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "", ErrNotFound
	}
	return string(results[0].Data), nil
}

func (m *macKeychain) List(projectID string) ([]string, error) {
	q := keychain.NewItem()
	q.SetSecClass(keychain.SecClassGenericPassword)
	q.SetService(ServiceName)
	q.SetMatchLimit(keychain.MatchLimitAll)
	q.SetReturnAttributes(true)

	results, err := keychain.QueryItem(q)
	if err != nil {
		return nil, err
	}
	var names []string
	prefix := projectID + "/"
	for _, r := range results {
		if len(r.Account) > len(prefix) && r.Account[:len(prefix)] == prefix {
			names = append(names, r.Account[len(prefix):])
		}
	}
	return names, nil
}

func (m *macKeychain) Delete(account string) error {
	q := keychain.NewItem()
	q.SetSecClass(keychain.SecClassGenericPassword)
	q.SetService(ServiceName)
	q.SetAccount(account)
	err := keychain.DeleteItem(q)
	if err == keychain.ErrorItemNotFound {
		return ErrNotFound
	}
	return err
}
