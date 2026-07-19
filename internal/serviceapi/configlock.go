package serviceapi

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/mdubb86/devm/internal/debuglog"
)

// configPaths returns devm.yaml and devm.me.yaml under repoRoot — the
// files the daemon holds host-immutable while the project's VM runs
// so a root guest can't tamper with them.
func configPaths(repoRoot string) []string {
	return []string{
		filepath.Join(repoRoot, "devm.yaml"),
		filepath.Join(repoRoot, "devm.me.yaml"),
	}
}

// lockConfigFiles makes devm.yaml (+ devm.me.yaml if present)
// host-immutable. Best-effort per file: a devm.me.yaml failure is
// logged and tolerated, but a devm.yaml failure is returned since
// that's the file the lock exists to protect.
func lockConfigFiles(repoRoot string) error {
	paths := configPaths(repoRoot)
	if err := setImmutable(paths[0], true); err != nil {
		return fmt.Errorf("lock devm.yaml: %w", err)
	}
	if err := setImmutable(paths[1], true); err != nil {
		debuglog.Logf("configlock", "lock devm.me.yaml: %v", err)
	}
	return nil
}

// unlockConfigFiles clears the immutable flag on both files (a
// missing file is a no-op). It always attempts both files and returns
// the first error encountered, if any.
func unlockConfigFiles(repoRoot string) error {
	var firstErr error
	for _, p := range configPaths(repoRoot) {
		if err := setImmutable(p, false); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// configLockEntry tracks one project's locked repo root and the
// pending timer (if any) scheduled to re-lock its config after a
// temporary unlock.
type configLockEntry struct {
	repoRoot string
	relock   *time.Timer
}

// configLockStore tracks each running project's config-lock state,
// mirroring softnetStore/ironProxyStore's mutex-guarded map pattern.
type configLockStore struct {
	mu sync.Mutex
	m  map[string]configLockEntry
}

func newConfigLockStore() *configLockStore {
	return &configLockStore{m: make(map[string]configLockEntry)}
}

func (s *configLockStore) put(projectID, repoRoot string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.m[projectID]
	e.repoRoot = repoRoot
	s.m[projectID] = e
}

func (s *configLockStore) get(projectID string) (configLockEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[projectID]
	return e, ok
}

// setTimer installs t as the project's pending relock timer, stopping
// and replacing whatever timer was previously scheduled so timers
// never leak or double-fire.
func (s *configLockStore) setTimer(projectID string, t *time.Timer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.m[projectID]
	if e.relock != nil {
		e.relock.Stop()
	}
	e.relock = t
	s.m[projectID] = e
}

func (s *configLockStore) stopTimer(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[projectID]
	if !ok || e.relock == nil {
		return
	}
	e.relock.Stop()
	e.relock = nil
	s.m[projectID] = e
}

func (s *configLockStore) del(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[projectID]; ok && e.relock != nil {
		e.relock.Stop()
	}
	delete(s.m, projectID)
}

var configLockState = newConfigLockStore()
