package serviceapi

import (
	"context"
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
	version    string
	mux        *http.ServeMux
}

// NewServer constructs a Server. socketPath should be SocketPath()
// in production; tests pass a temp path. version is the build
// version reported by /version.
func NewServer(socketPath, version string) *Server {
	s := &Server{
		socketPath: socketPath,
		version:    version,
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
	fmt.Fprintf(w, "{\"version\":%q}\n", s.version)
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
