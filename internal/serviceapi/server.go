package serviceapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
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
}

// Build describes the daemon binary's build identity, reported via
// /version. Version is the semver tag for release builds, "dev" for
// working-tree builds. Commit is the git rev at build time, with a
// "-dirty" suffix when the working tree had uncommitted changes.
// Date is the ISO8601 build timestamp.
//
// Commit drives dev-loop drift detection: a CLI whose embedded Commit
// differs from the daemon's reported Commit knows the daemon needs a
// restart. Version isn't enough for that — every dev build reports
// Version="dev".
type Build struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
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
