package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"
)

// runsMain implements `gdevm runs`. GETs /v1/runs from gdevm serve
// and prints one row per run: NAME, PID, STARTED, MODE, STATUS. For
// exited runs STATUS shows the exit code; for live runs it shows
// "running". Returns 0 on success.
func runsMain(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: gdevm runs")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), shortClientTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientURL("/v1/runs"), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm runs: build request: %v\n", err)
		return 1
	}
	resp, err := clientDo(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm runs: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(os.Stderr, resp.Body)
		return 1
	}
	var rows []runSummary
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		fmt.Fprintf(os.Stderr, "gdevm runs: parse response: %v\n", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Println("no runs")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPID\tSTARTED\tMODE\tSTATUS")
	for _, r := range rows {
		mode := "attached"
		if r.Detach {
			mode = "detached"
		}
		if r.Orphan {
			mode = "orphan"
		}
		status := "running"
		if r.Exited {
			if r.Orphan {
				// Exit code is unknowable for a rediscovered run —
				// we weren't the parent, so wait() never ran.
				status = "exit ?"
			} else {
				status = fmt.Sprintf("exit %d", r.ExitCode)
			}
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", r.Name, r.Pid, r.StartedAt.Format(time.RFC3339), mode, status)
	}
	_ = tw.Flush()
	return 0
}
