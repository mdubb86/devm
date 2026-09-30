package serviceapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

// Server is the HTTP API the devm service exposes over a Unix
// domain socket. Ship 1 only registers /health and /version; later
// ships add endpoints via Register.
type Server struct {
	socketPath string
	build      Build
	mux        *http.ServeMux
	cache      *StateCache
}

// Build describes the daemon binary's build identity, reported via
// /version. Version is the semver tag for release builds, "dev" for
// working-tree builds. Commit is the git rev at build time, with a
// "-dirty" suffix when the working tree had uncommitted changes.
// Date is the ISO8601 build timestamp.
//
// Fingerprint is a per-build random stamp injected via
// `-ldflags "-X main.Fingerprint=<random>"` (empty or "dev" for a
// local `go build`). Two processes that share a Fingerprint were
// compiled from the same binary; different Fingerprints mean the
// on-disk binary has been rebuilt since the daemon last started.
// Used both for daemon↔CLI drift detection (`devm version --json`
// vs `/version`) and as the guest bundle-refresh fingerprint that
// reconcile stamps into state to detect a stale in-guest bundle.
//
// Commit drives release-build drift detection: a CLI whose embedded
// Commit differs from the daemon's reported Commit knows the daemon
// needs a restart. For dev builds Commit is "none" for both, so
// Fingerprint carries the discrimination.
//
// BinaryPath is the filesystem path the daemon process was launched
// from (os.Executable() at startup, symlinks resolved). Reported so
// the CLI can decide, on a Fingerprint drift, whether the invoked
// binary is the same on-disk file as the daemon's — a "rebuild in
// place" that can be auto-healed by restarting the daemon — versus a
// different binary entirely (dev bin/devm vs prod ~/.local/bin/devm),
// where auto-restart would silently flip the installed daemon.
type Build struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	Date        string `json:"date"`
	Fingerprint string `json:"fingerprint,omitempty"`
	BinaryPath  string `json:"binary_path,omitempty"`
}

// NewServer constructs a Server. socketPath should be SocketPath()
// in production; tests pass a temp path. build is the binary's
// build identity, reported via /version.
func NewServer(socketPath string, build Build) *Server {
	s := &Server{
		socketPath: socketPath,
		build:      build,
		mux:        http.NewServeMux(),
	}
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/version", s.handleVersion)
	return s
}

// SetStateCache wires the daemon's StateCache into the server so
// handleVersion can read it. Called by runner.go once the cache is
// constructed and warmed.
func (s *Server) SetStateCache(cache *StateCache) {
	s.cache = cache
}

// Register adds a handler at the given pattern. Used by later ships
// to add their own endpoints onto the same socket.
func (s *Server) Register(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	build := s.build
	if s.cache != nil {
		build = s.cache.Global().Build
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(build)
}

// Serve binds the Unix socket and serves until ctx is cancelled.
// Removes any stale socket file before binding. Enforces 0600 perms.
func (s *Server) Serve(ctx context.Context) error {
	// Clean up any stale socket from a prior process that didn't
	// shut down cleanly. A "left behind" socket file blocks bind.
	_ = os.Remove(s.socketPath)

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", s.socketPath, err)
	}
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod %s: %w", s.socketPath, err)
	}
	log.Printf("serviceapi: listening on %s", s.socketPath)

	server := &http.Server{Handler: s.mux}

	// Shutdown on ctx done.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = os.Remove(s.socketPath)
	}()

	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
