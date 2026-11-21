package serviceapi

import (
	"context"
)

// reconcileLAN starts or stops the LAN listener based on whether any
// ExposeHost routes exist in the table. Called after every Apply/Remove
// so the listener lifecycle tracks the opt-in set, and once during
// daemon-startup rehydration so recovered ExposeHost routes rebind it.
func reconcileLAN(ctx context.Context, proxy *ProxyServer, routes *Routes, lanPort int) error {
	if routes.CountLANRoutes() > 0 {
		return proxy.StartLANListener(ctx, lanPort)
	}
	proxy.StopLANListener()
	return nil
}
