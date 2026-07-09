package serviceapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/mdubb86/devm/internal/debuglog"
)

// Server is the HTTP API the devm service exposes over a Unix
// domain socket. Ship 1 only registers /health and /version; later
// ships add endpoints via Register.
type Server struct {
	socketPath string
	build      Build
	mux        *http.ServeMux
	proxyReady atomic.Bool
}

// Build describes the daemon binary's build identity, reported via
// /version. Version is the semver tag for release builds, "dev" for
// working-tree builds. Commit is the git rev at build time, with a
// "-dirty" suffix when the working tree had uncommitted changes.
// Date is the ISO8601 build timestamp.
//
// Fingerprint is a content-hash of os.Executable() computed at
// startup — the same value from the CLI's `devm version --json` and
// the daemon's `/version`. Two processes running byte-for-byte
// identical binaries produce the same Fingerprint; a rebuild that
// changes any bit produces a different one. Test infra uses this to
// decide whether the daemon is up-to-date without paying for a
// reinstall on every run: if CLI.Fingerprint == daemon.Fingerprint,
// the daemon is running the code the CLI just built.
//
// Commit drives release-build drift detection: a CLI whose embedded
// Commit differs from the daemon's reported Commit knows the daemon
// needs a restart. For dev builds Commit is "none" for both, so
// Fingerprint carries the discrimination.
type Build struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	Date        string `json:"date"`
	Fingerprint string `json:"fingerprint,omitempty"`
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
	s.mux.HandleFunc("/proxy-status", s.handleProxyStatus)
	return s
}

// SetProxyReady toggles the reverse-proxy actor's ready state. Called
// by runner.go once launchd's :80/:443 listeners have been handed off
// and the actor is serving. Cleared on daemon shutdown by process
// exit; no explicit unset — a running daemon whose proxy actor
// crashed intentionally keeps the flag true so the CLI still surfaces
// the crash instead of pretending nothing was ever running.
func (s *Server) SetProxyReady(ready bool) {
	s.proxyReady.Store(ready)
}

// handleProxyStatus returns {"ready":bool} — was the reverse-proxy
// actor started this daemon's lifetime. Used by `devm status` in place
// of a raw TCP dial to 127.0.0.1:443 (which drops the connection
// mid-TLS handshake and spams the daemon log with "TLS handshake
// error … EOF").
func (s *Server) handleProxyStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ready": s.proxyReady.Load()})
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(s.build)
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
	debuglog.Logf("serviceapi", "listening on %s", s.socketPath)

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
