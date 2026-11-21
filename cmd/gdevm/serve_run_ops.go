package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// handleStop implements POST /v1/stop?name=X. Sends SIGTERM to the
// run's process group, waits up to gracefulStopTimeout, then SIGKILL.
// Returns 204 on success, 404 when name is unknown.
func handleStop(reg *runRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		r1 := reg.lookup(name)
		if r1 == nil {
			http.Error(w, fmt.Sprintf("no active run named %q", name), http.StatusNotFound)
			return
		}
		stopRun(r1)
		w.WriteHeader(http.StatusNoContent)
	}
}

// runSummary is one row of GET /v1/runs. ExitCode is zero-valued
// while the run is still live; Exited reports whether the row is
// post-exit (so a zero code is distinguishable from "still running").
// Orphan means the row was rediscovered after a gdevm serve restart
// — we're not the parent, so ExitCode of an exited orphan is -1
// ("unknown") rather than the real exit status.
type runSummary struct {
	Name      string    `json:"name"`
	Pid       int       `json:"pid"`
	Pgid      int       `json:"pgid"`
	StartedAt time.Time `json:"started_at"`
	Detach    bool      `json:"detach"`
	Orphan    bool      `json:"orphan,omitempty"`
	Exited    bool      `json:"exited"`
	ExitCode  int       `json:"exit_code,omitempty"`
}

// handleListRuns implements GET /v1/runs.
func handleListRuns(reg *runRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		runs := reg.list()
		out := make([]runSummary, 0, len(runs))
		for _, r1 := range runs {
			s := runSummary{
				Name:      r1.name,
				Pid:       leaderPid(r1),
				Pgid:      r1.pgid,
				StartedAt: r1.startedAt,
				Detach:    r1.detach,
				Orphan:    isOrphan(r1),
			}
			select {
			case <-r1.exitDone:
				s.Exited = true
				s.ExitCode = r1.exitCode
			default:
			}
			out = append(out, s)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// handleLogs implements GET /v1/logs?name=X&follow=true. Serves the
// merged stdout+stderr log file for the named run. When follow=true,
// on EOF the handler polls every logsFollowPollInterval for new bytes
// and exits when the run has exited AND there's no more to read.
func handleLogs(reg *runRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		follow := r.URL.Query().Get("follow") == "true"

		r1 := reg.lookup(name)
		logPath := logFileForName(name)
		f, err := os.Open(logPath)
		if err != nil {
			if os.IsNotExist(err) && r1 == nil {
				http.Error(w, fmt.Sprintf("no run or log for %q", name), http.StatusNotFound)
				return
			}
			http.Error(w, "open log: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		buf := make([]byte, 4096)
		for {
			n, readErr := f.Read(buf)
			if n > 0 {
				if _, err := w.Write(buf[:n]); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if readErr == io.EOF || (readErr == nil && n == 0) {
				if !follow {
					return
				}
				// Follow mode: on EOF, wait briefly and retry. Exit
				// when the run has exited AND the file is still at
				// EOF (final drain already happened).
				var exited bool
				if r1 != nil {
					select {
					case <-r1.exitDone:
						exited = true
					default:
					}
				} else {
					// No live run, but the log exists — treat as
					// terminated.
					exited = true
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(logsFollowPollInterval):
				}
				if exited {
					// One final drain pass to catch bytes written
					// between EOF and exit.
					for {
						tn, _ := f.Read(buf)
						if tn == 0 {
							return
						}
						if _, err := w.Write(buf[:tn]); err != nil {
							return
						}
						if flusher != nil {
							flusher.Flush()
						}
					}
				}
				continue
			}
			if readErr != nil {
				return
			}
		}
	}
}

// logsFollowPollInterval is the sleep between EOF-retry reads in
// follow mode. 100ms balances responsiveness against polling cost
// when a run is idle. Package var so tests can shrink it.
var logsFollowPollInterval = 100 * time.Millisecond

// logFileForName is the on-disk path of a run's merged output log.
func logFileForName(name string) string {
	return fmt.Sprintf("%s/%s.log", logsDir, name)
}
