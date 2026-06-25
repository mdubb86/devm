package serviceapi

import (
	"context"
	"fmt"

	"github.com/oklog/run"
)

// RunService composes the service's goroutines into an oklog/run
// group and blocks until any actor returns. Ship 1 only runs the
// HTTP server; later ships add: DNS responder, iron-proxy supervisor,
// Caddy embed, sandbox watcher.
//
// version is the build version reported by /version. ctx is the
// shutdown signal: cancel it and every actor stops.
func RunService(ctx context.Context, version string) error {
	if _, err := EnsureRuntimeDir(); err != nil {
		return fmt.Errorf("ensure runtime dir: %w", err)
	}

	server := NewServer(SocketPath(), version)

	var g run.Group

	// HTTP server actor.
	{
		serverCtx, cancel := context.WithCancel(ctx)
		g.Add(func() error {
			return server.Serve(serverCtx)
		}, func(error) {
			cancel()
		})
	}

	// Context-cancel actor: when ctx is cancelled (parent signal),
	// the group returns.
	{
		ctxCancel := make(chan struct{})
		g.Add(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ctxCancel:
				return nil
			}
		}, func(error) {
			close(ctxCancel)
		})
	}

	return g.Run()
}
