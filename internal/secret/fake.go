package secret

import (
	"sort"
	"strings"
	"sync"
)

// NewFake returns an in-memory Backend for tests.
func NewFake() Backend { return &fakeBackend{store: map[string]string{}} }

type fakeBackend struct {
	mu    sync.Mutex
	store map[string]string
}

func (f *fakeBackend) Set(account, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store[account] = value
	return nil
}

func (f *fakeBackend) Get(account string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.store[account]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *fakeBackend) List(projectID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := projectID + "/"
	var names []string
	for k := range f.store {
		if strings.HasPrefix(k, prefix) {
			names = append(names, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (f *fakeBackend) Delete(account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.store[account]; !ok {
		return ErrNotFound
	}
	delete(f.store, account)
	return nil
}
