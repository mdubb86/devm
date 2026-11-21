package main

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

type healthResponse struct {
	OK            bool `json:"ok"`
	UptimeSeconds int  `json:"uptime_seconds"`
	PID           int  `json:"pid"`
}

// healthHandler serves the guest daemon's liveness probe: the Mac-side
// watchdog hits this to confirm gdevm serve is up and reachable.
func healthHandler(started time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(healthResponse{
			OK:            true,
			UptimeSeconds: int(time.Since(started).Seconds()),
			PID:           os.Getpid(),
		})
	})
}
