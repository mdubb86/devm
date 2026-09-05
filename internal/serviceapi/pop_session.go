package serviceapi

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mdubb86/devm/internal/identity"
)

// PopKind distinguishes whether a PopSession syncs a single file or a
// whole directory from the guest.
type PopKind int

const (
	PopKindFile PopKind = 0
	PopKindDir  PopKind = 1
)

// PopSession is one live guest→Mac scratch sync for a path that isn't
// inside any devm-managed mirror — the state pop's out-of-mirror
// branch tracks per session.
type PopSession struct {
	ID               string
	ProjectName      string
	GuestPath        string
	Kind             PopKind
	MacDir           string
	TargetName       string
	MutagenSessionID string
	CreatedAt        time.Time
}

func popSessionID(canonicalGuestPath string) string {
	sum := sha256.Sum256([]byte(canonicalGuestPath))
	return hex.EncodeToString(sum[:])[:12]
}

func popSessionMutagenName(projectName, id string) string {
	return "pop-" + projectName + "-" + id
}

// PopScratchRoot returns <RuntimeDir>/pop-tmp/.
func PopScratchRoot(cfg identity.Config) string {
	return filepath.Join(cfg.RuntimeDir(), "pop-tmp")
}

// MacDirForPopSession returns the per-session mount target under
// PopScratchRoot(cfg).
func MacDirForPopSession(cfg identity.Config, id string) string {
	return filepath.Join(PopScratchRoot(cfg), id)
}

// PopSessionStore tracks live pop temp-sessions, keyed by canonical
// guest path.
type PopSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*PopSession
}

func NewPopSessionStore() *PopSessionStore {
	return &PopSessionStore{sessions: make(map[string]*PopSession)}
}

func (s *PopSessionStore) Get(guestPath string) (*PopSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.sessions[guestPath]; ok {
		cp := *v
		return &cp, true
	}
	return nil, false
}

// GetOrCreate returns the existing session for guestPath, or builds a
// fresh one (ID + MacDir + TargetName filled) and calls create so the
// caller can populate MutagenSessionID and any other transport-owned
// fields. On create-error the session is NOT stored and the error is
// returned. Under lock throughout — dedupes concurrent creates for the
// same guestPath.
func (s *PopSessionStore) GetOrCreate(
	cfg identity.Config,
	projectName, guestPath string,
	kind PopKind,
	create func(*PopSession) error,
) (session *PopSession, created bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if v, ok := s.sessions[guestPath]; ok {
		cp := *v
		return &cp, false, nil
	}

	id := popSessionID(guestPath)
	ps := &PopSession{
		ID:          id,
		ProjectName: projectName,
		GuestPath:   guestPath,
		Kind:        kind,
		MacDir:      MacDirForPopSession(cfg, id),
		CreatedAt:   time.Now(),
	}
	if kind == PopKindFile {
		ps.TargetName = filepath.Base(guestPath)
	}
	if err := create(ps); err != nil {
		return nil, false, err
	}
	s.sessions[guestPath] = ps
	cp := *ps
	return &cp, true, nil
}

// RemoveByID removes and returns the session with matching ID. Returns
// nil if none matched.
func (s *PopSessionStore) RemoveByID(id string) *PopSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, v := range s.sessions {
		if v.ID == id {
			delete(s.sessions, key)
			cp := *v
			return &cp
		}
	}
	return nil
}

// ListForProject returns copies of every session for projectName,
// sorted by CreatedAt ascending (oldest first).
func (s *PopSessionStore) ListForProject(projectName string) []PopSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PopSession
	for _, v := range s.sessions {
		if v.ProjectName == projectName {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// All returns copies of every session.
func (s *PopSessionStore) All() []PopSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PopSession, 0, len(s.sessions))
	for _, v := range s.sessions {
		out = append(out, *v)
	}
	return out
}
