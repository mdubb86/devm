package serviceapi

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/mutagen"
)

const (
	defaultPopSessionTTL        = time.Hour
	defaultPopSessionGCInterval = 5 * time.Minute
)

func PopSessionTTL() time.Duration {
	return envDurationSeconds("DEVM_POP_SESSION_TTL_SECONDS", defaultPopSessionTTL)
}

func PopSessionGCInterval() time.Duration {
	return envDurationSeconds("DEVM_POP_SESSION_GC_INTERVAL_SECONDS", defaultPopSessionGCInterval)
}

func envDurationSeconds(name string, dflt time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return dflt
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return dflt
	}
	return time.Duration(n) * time.Second
}

// popSessionLastChangeAt returns the newest ModTime among files under
// macDir (walking the tree). Returns macDir's own mtime when the dir is
// empty; zero time on stat error. This is the "did mutagen write here
// recently?" signal that drives GC — deliberately mtime-based rather
// than parsing mutagen's session JSON internals.
func popSessionLastChangeAt(macDir string) time.Time {
	info, err := os.Stat(macDir)
	if err != nil {
		return time.Time{}
	}
	newest := info.ModTime()
	_ = filepath.Walk(macDir, func(_ string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable subtrees
		}
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	return newest
}

// GCPopSessionsOnce sweeps sessions whose last-change age is >= ttl.
// Returns the sessions that were torn down.
func GCPopSessionsOnce(
	store *PopSessionStore,
	cli *mutagen.CLI,
	cfg identity.Config,
	ttl time.Duration,
	now func() time.Time,
) []PopSession {
	var removed []PopSession
	for _, ps := range store.All() {
		last := popSessionLastChangeAt(ps.MacDir)
		if last.IsZero() {
			last = ps.CreatedAt // never observed a write — age from creation
		}
		if now().Sub(last) < ttl {
			continue
		}
		got := store.RemoveByID(ps.ID)
		if got == nil {
			continue // raced with another teardown path
		}
		if err := TearDownPopSyncSession(cli, cfg, *got); err != nil {
			daemonlog.Errorf("pop session %s: tear down (gc): %v", got.ID, err)
		}
		log.Printf("pop session %s: gc'd (idle %s) project=%s path=%s",
			got.ID, now().Sub(last).Round(time.Second), got.ProjectName, got.GuestPath)
		removed = append(removed, *got)
	}
	return removed
}

// RunPopSessionGC is a long-running actor: sweeps every interval; exits
// on ctx.Done() returning ctx.Err().
func RunPopSessionGC(
	ctx context.Context,
	store *PopSessionStore,
	cli *mutagen.CLI,
	cfg identity.Config,
	ttl, interval time.Duration,
) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			GCPopSessionsOnce(store, cli, cfg, ttl, time.Now)
		}
	}
}

// SweepAllPopSessions tears down every session in the store. Called at
// daemon shutdown.
func SweepAllPopSessions(store *PopSessionStore, cli *mutagen.CLI, cfg identity.Config) {
	for _, ps := range store.All() {
		got := store.RemoveByID(ps.ID)
		if got == nil {
			continue
		}
		if err := TearDownPopSyncSession(cli, cfg, *got); err != nil {
			daemonlog.Errorf("pop session %s: tear down (shutdown sweep): %v", got.ID, err)
		}
	}
}

// SweepProjectPopSessions tears down every session belonging to
// projectName. Called from /vm/stop and /vm/teardown before releasing
// the project's VM.
func SweepProjectPopSessions(store *PopSessionStore, cli *mutagen.CLI, cfg identity.Config, projectName string) {
	for _, ps := range store.ListForProject(projectName) {
		got := store.RemoveByID(ps.ID)
		if got == nil {
			continue
		}
		if err := TearDownPopSyncSession(cli, cfg, *got); err != nil {
			daemonlog.Errorf("pop session %s: tear down (project sweep): %v", got.ID, err)
		}
	}
}

// WipePopScratchOnStartup removes the entire pop-tmp/ dir. Any pop
// sessions from a prior daemon are dead by definition (their mutagen
// agents are gone with the guest process tree) — no adopt logic.
func WipePopScratchOnStartup(cfg identity.Config) error {
	err := os.RemoveAll(PopScratchRoot(cfg))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
