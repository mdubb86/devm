package secret

import "github.com/keybase/go-keychain"

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
