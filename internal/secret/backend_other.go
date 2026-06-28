//go:build !darwin

package secret

import "errors"

var errUnsupported = errors.New("secret: macOS keychain backend not available on this platform")

func (m *macKeychain) Set(account, value string) error          { return errUnsupported }
func (m *macKeychain) Get(account string) (string, error)       { return "", errUnsupported }
func (m *macKeychain) List(projectID string) ([]string, error)  { return nil, errUnsupported }
func (m *macKeychain) Delete(account string) error              { return errUnsupported }
