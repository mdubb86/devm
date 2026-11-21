package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Guest-side snapshot CLI. Each subcommand POSTs or GETs one route on
// the daemon's per-project guest-API listener (softnet-forwarded from
// guest TCP 192.168.127.1:81). Bodies and queries omit `project`: the
// listener pins it at registration (perProjectName in
// serviceapi.registerSnapshotRoutes), so there is one project per
// listener and the caller can't address a different one anyway.
var (
	snapshotCreateEndpoint  = "http://192.168.127.1:81/snapshot/create"
	snapshotRestoreEndpoint = "http://192.168.127.1:81/snapshot/restore"
	snapshotListEndpoint    = "http://192.168.127.1:81/snapshot/list"
	snapshotPruneEndpoint   = "http://192.168.127.1:81/snapshot/prune"
	snapshotShowEndpoint    = "http://192.168.127.1:81/snapshot/show"
	snapshotClearEndpoint   = "http://192.168.127.1:81/snapshot/clear"
)

const snapshotUsage = `usage: gdevm snapshot <subcommand> [args...]

Subcommands:
  create <name> [--source cli|cron]   Capture a snapshot for the named entry.
  restore <name> [<ts>] [--force]     Restore <name> from <ts> (defaults to latest).
  list [<name>]                       List snapshot archives (all entries when omitted).
  prune <name> --keep <N>             Remove older archives, keeping the newest N.
  show <name> [<ts>]                  Print one snapshot's metadata.
  clear <name>                        Delete all archives for the named entry.
`

// snapshotMeta mirrors internal/snapshot.Meta's wire shape. Kept local
// so gdevm doesn't pull the Mac-side snapshot package into the guest
// binary.
//
// Mode, Status, CapturedPath, IsDir, Orphan are absent on v0.31
// sidecars — the daemon omits them from the JSON. effectiveMode()
// defaults Mode to "stream" so the column always reads as a decision.
type snapshotMeta struct {
	TS           string `json:"ts"`
	HookName     string `json:"hook_name"`
	ArchiveName  string `json:"archive_name"`
	Source       string `json:"source"`
	ExitCode     int    `json:"exit_code"`
	DurationMs   int64  `json:"duration_ms"`
	SizeBytes    int64  `json:"size_bytes"`
	Mode         string `json:"mode,omitempty"`
	CapturedPath string `json:"captured_path,omitempty"`
	IsDir        bool   `json:"is_dir,omitempty"`
	Status       string `json:"status,omitempty"`
	Orphan       bool   `json:"orphan,omitempty"`
}

// effectiveMode resolves a v0.31 sidecar's empty mode to "stream" so
// the column always reads as a decision — mirrors Meta.EffectiveMode
// on the Mac side.
func (m snapshotMeta) effectiveMode() string {
	if m.Mode == "" {
		return "stream"
	}
	return m.Mode
}

// effectiveStatus resolves an empty status to "ok" so the column
// always reads as a decision — mirrors listStatus on the Mac side.
func (m snapshotMeta) effectiveStatus() string {
	if m.Status == "" {
		return "ok"
	}
	return m.Status
}

// listName picks the NAME column value for a row. ArchiveName is the
// entry name on fresh captures (and on the synthetic orphan rows the
// handler builds), but older sidecars stored the hook name — strip a
// "backup_" prefix so the column stays readable either way.
func (m snapshotMeta) listName() string {
	name := m.ArchiveName
	if name == "" {
		name = m.HookName
	}
	return strings.TrimPrefix(name, "backup_")
}

type snapshotCreateBody struct {
	Entry  string `json:"entry"`
	Source string `json:"source"`
}

type snapshotRestoreBody struct {
	Entry string `json:"entry"`
	TS    string `json:"ts"`
	Force bool   `json:"force"`
}

type snapshotClearBody struct {
	Entry string `json:"entry"`
}

type snapshotClearResponse struct {
	Removed int `json:"removed"`
}

type snapshotPruneBody struct {
	Entry string `json:"entry"`
	Keep  int    `json:"keep"`
}

type snapshotPruneResponse struct {
	Removed []string `json:"removed"`
}

type snapshotRestoreResponse struct {
	Restored bool `json:"restored"`
}

// snapshotMain dispatches `gdevm snapshot <sub>`.
func snapshotMain(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, snapshotUsage)
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "create":
		return snapshotCreateCmdMain(rest)
	case "restore":
		return snapshotRestoreCmdMain(rest)
	case "list":
		return snapshotListCmdMain(rest)
	case "prune":
		return snapshotPruneCmdMain(rest)
	case "show":
		return snapshotShowCmdMain(rest)
	case "clear":
		return snapshotClearCmdMain(rest)
	case "-h", "--help":
		fmt.Print(snapshotUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "gdevm snapshot: unknown subcommand %q\n%s", sub, snapshotUsage)
		return 2
	}
}

func snapshotCreateCmdMain(args []string) int {
	entry := ""
	source := "cli"
	sawSource := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--source":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "gdevm snapshot create: --source requires a value")
				return 2
			}
			source = args[i+1]
			sawSource = true
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "gdevm snapshot create: unknown flag %q\n", args[i])
				return 2
			}
			if entry != "" {
				fmt.Fprintf(os.Stderr, "gdevm snapshot create: unexpected arg %q\n", args[i])
				return 2
			}
			entry = args[i]
		}
	}
	if entry == "" {
		fmt.Fprint(os.Stderr, "gdevm snapshot create: usage: create <name> [--source cli|cron]\n")
		return 2
	}
	if sawSource && source != "cli" && source != "cron" {
		fmt.Fprintf(os.Stderr, "gdevm snapshot create: --source must be %q or %q, got %q\n", "cli", "cron", source)
		return 2
	}
	body, _ := json.Marshal(snapshotCreateBody{Entry: entry, Source: source})
	respBody, code := snapshotPOST("create", snapshotCreateEndpoint, body)
	if code != 0 {
		return code
	}
	var meta snapshotMeta
	if err := json.Unmarshal(respBody, &meta); err != nil {
		fmt.Fprintf(os.Stderr, "gdevm snapshot create: decode response: %v\n", err)
		return 1
	}
	fmt.Printf("snapshot %s saved as %s (%s)\n", entry, meta.TS, formatSnapshotSize(meta.SizeBytes))
	return 0
}

func snapshotRestoreCmdMain(args []string) int {
	entry := ""
	ts := ""
	force := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force":
			force = true
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "gdevm snapshot restore: unknown flag %q\n", args[i])
				return 2
			}
			switch {
			case entry == "":
				entry = args[i]
			case ts == "":
				ts = args[i]
			default:
				fmt.Fprintf(os.Stderr, "gdevm snapshot restore: unexpected arg %q\n", args[i])
				return 2
			}
		}
	}
	if entry == "" {
		fmt.Fprint(os.Stderr, "gdevm snapshot restore: usage: restore <name> [<ts>] [--force]\n")
		return 2
	}
	body, _ := json.Marshal(snapshotRestoreBody{Entry: entry, TS: ts, Force: force})
	respBody, code := snapshotPOST("restore", snapshotRestoreEndpoint, body)
	if code != 0 {
		return code
	}
	var resp snapshotRestoreResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "gdevm snapshot restore: decode response: %v\n", err)
		return 1
	}
	if !resp.Restored {
		fmt.Fprintf(os.Stderr, "no snapshot to restore for %s\n", entry)
		return 0
	}
	displayTS := ts
	if displayTS == "" {
		displayTS = "latest"
	}
	fmt.Printf("restored %s from %s\n", entry, displayTS)
	return 0
}

func snapshotClearCmdMain(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, "gdevm snapshot clear: usage: clear <name>\n")
		return 2
	}
	entry := args[0]
	if strings.HasPrefix(entry, "-") {
		fmt.Fprintf(os.Stderr, "gdevm snapshot clear: unknown flag %q\n", entry)
		return 2
	}
	body, _ := json.Marshal(snapshotClearBody{Entry: entry})
	respBody, code := snapshotPOST("clear", snapshotClearEndpoint, body)
	if code != 0 {
		return code
	}
	var resp snapshotClearResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "gdevm snapshot clear: decode response: %v\n", err)
		return 1
	}
	fmt.Printf("cleared %d archive(s) for %s\n", resp.Removed, entry)
	return 0
}

func snapshotListCmdMain(args []string) int {
	if len(args) > 1 {
		fmt.Fprint(os.Stderr, "gdevm snapshot list: usage: list [<name>]\n")
		return 2
	}
	endpoint := snapshotListEndpoint
	hasEntry := len(args) == 1
	if hasEntry {
		endpoint += "?" + url.Values{"entry": {args[0]}}.Encode()
	}
	respBody, code := snapshotGET("list", endpoint)
	if code != 0 {
		return code
	}
	var metas []snapshotMeta
	if err := json.Unmarshal(respBody, &metas); err != nil {
		fmt.Fprintf(os.Stderr, "gdevm snapshot list: decode response: %v\n", err)
		return 1
	}
	if hasEntry {
		// Per-archive rows for one entry: TS  SIZE  SOURCE. Mirrors the
		// Mac CLI's per-entry view — operators who know the entry name
		// usually want the archive timeline, not the summary.
		for _, m := range metas {
			source := m.Source
			if source == "" {
				source = "unknown"
			}
			fmt.Printf("%s  %s  %s\n", m.TS, formatSnapshotSize(m.SizeBytes), source)
		}
		return 0
	}
	// No entry — one row per declared entry plus any orphan dirs the
	// daemon found on disk. Tabulated so operators can scan mode and
	// status at a glance; the ORPHAN column only appears when at least
	// one row needs it.
	hasOrphan := false
	for _, m := range metas {
		if m.Orphan {
			hasOrphan = true
			break
		}
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	header := "NAME\tMODE\tSTATUS\tLATEST\tSIZE\tSOURCE"
	if hasOrphan {
		header += "\tORPHAN"
	}
	fmt.Fprintln(tw, header)
	for _, m := range metas {
		source := m.Source
		if source == "" {
			source = "unknown"
		}
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s",
			m.listName(),
			m.effectiveMode(),
			m.effectiveStatus(),
			formatSnapshotTSHuman(m.TS),
			formatSnapshotSize(m.SizeBytes),
			source,
		)
		if hasOrphan {
			cell := ""
			if m.Orphan {
				cell = "orphan"
			}
			row += "\t" + cell
		}
		fmt.Fprintln(tw, row)
	}
	return nonzeroOn(tw.Flush())
}

// nonzeroOn maps an error to exit code 1 (or 0 when nil) so a flush
// failure does not get silently swallowed. The daemon's wire format is
// well-bounded, so this is a latent-io guard more than a likely case.
func nonzeroOn(err error) int {
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm snapshot: %v\n", err)
		return 1
	}
	return 0
}

func snapshotPruneCmdMain(args []string) int {
	entry := ""
	keep := 0
	sawKeep := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--keep":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "gdevm snapshot prune: --keep requires a value")
				return 2
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				fmt.Fprintf(os.Stderr, "gdevm snapshot prune: --keep must be a positive integer, got %q\n", args[i+1])
				return 2
			}
			keep = n
			sawKeep = true
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "gdevm snapshot prune: unknown flag %q\n", args[i])
				return 2
			}
			if entry != "" {
				fmt.Fprintf(os.Stderr, "gdevm snapshot prune: unexpected arg %q\n", args[i])
				return 2
			}
			entry = args[i]
		}
	}
	if entry == "" {
		fmt.Fprintln(os.Stderr, "gdevm snapshot prune: <name> is required")
		return 2
	}
	if !sawKeep {
		fmt.Fprintln(os.Stderr, "gdevm snapshot prune: --keep is required")
		return 2
	}
	body, _ := json.Marshal(snapshotPruneBody{Entry: entry, Keep: keep})
	respBody, code := snapshotPOST("prune", snapshotPruneEndpoint, body)
	if code != 0 {
		return code
	}
	var pr snapshotPruneResponse
	if err := json.Unmarshal(respBody, &pr); err != nil {
		fmt.Fprintf(os.Stderr, "gdevm snapshot prune: decode response: %v\n", err)
		return 1
	}
	fmt.Printf("pruned %d snapshot(s)\n", len(pr.Removed))
	return 0
}

func snapshotShowCmdMain(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprint(os.Stderr, "gdevm snapshot show: usage: show <name> [<ts>]\n")
		return 2
	}
	entry := args[0]
	q := url.Values{"entry": {entry}}
	if len(args) == 2 {
		q.Set("ts", args[1])
	}
	endpoint := snapshotShowEndpoint + "?" + q.Encode()
	respBody, code := snapshotGET("show", endpoint)
	if code != 0 {
		return code
	}
	var meta snapshotMeta
	if err := json.Unmarshal(respBody, &meta); err != nil {
		fmt.Fprintf(os.Stderr, "gdevm snapshot show: decode response: %v\n", err)
		return 1
	}
	source := meta.Source
	if source == "" {
		source = "unknown"
	}
	fmt.Printf("ts:            %s\n", meta.TS)
	fmt.Printf("hook_name:     %s\n", meta.HookName)
	fmt.Printf("archive_name:  %s\n", meta.ArchiveName)
	fmt.Printf("source:        %s\n", source)
	fmt.Printf("mode:          %s\n", meta.effectiveMode())
	fmt.Printf("status:        %s\n", meta.effectiveStatus())
	if meta.CapturedPath != "" {
		fmt.Printf("captured_path: %s\n", meta.CapturedPath)
		fmt.Printf("is_dir:        %t\n", meta.IsDir)
	}
	if meta.Orphan {
		fmt.Printf("orphan:        true\n")
	}
	fmt.Printf("exit_code:     %d\n", meta.ExitCode)
	fmt.Printf("duration:      %s\n", (time.Duration(meta.DurationMs) * time.Millisecond).String())
	fmt.Printf("size:          %s (%d bytes)\n", formatSnapshotSize(meta.SizeBytes), meta.SizeBytes)
	return 0
}

// snapshotPOST sends body as JSON and returns (response, 0) on 2xx or
// (nil, non-zero exit) with a stderr message on failure.
func snapshotPOST(label, endpoint string, body []byte) ([]byte, int) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm snapshot %s: %v\n", label, err)
		return nil, 1
	}
	req.Header.Set("Content-Type", "application/json")
	// Capture and restore run hooks inside the guest — they can take
	// minutes for a large volume. 10 minutes is a loose ceiling.
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm snapshot %s: cannot reach devm daemon on 192.168.127.1:81 — is the VM properly started?\n%v\n", label, err)
		return nil, 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return respBody, 0
	}
	fmt.Fprintf(os.Stderr, "gdevm snapshot %s: %s\n", label, extractSnapshotErr(respBody, resp.StatusCode))
	return nil, 1
}

// snapshotGET is snapshotPOST without a body.
func snapshotGET(label, endpoint string) ([]byte, int) {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm snapshot %s: %v\n", label, err)
		return nil, 1
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm snapshot %s: cannot reach devm daemon on 192.168.127.1:81 — is the VM properly started?\n%v\n", label, err)
		return nil, 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return respBody, 0
	}
	fmt.Fprintf(os.Stderr, "gdevm snapshot %s: %s\n", label, extractSnapshotErr(respBody, resp.StatusCode))
	return nil, 1
}

// extractSnapshotErr unpacks the daemon's {"error":"..."} payload or
// falls back to the raw body with the status code.
func extractSnapshotErr(body []byte, status int) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return fmt.Sprintf("daemon returned %d: %s", status, strings.TrimSpace(string(body)))
}

// formatSnapshotSize renders bytes as a base-1024 string with one
// fractional digit. Mirrors cmd/devm's formatSize; cmd-package functions
// aren't importable into cmd/gdevm, so the small body lives here too.
func formatSnapshotSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	f := float64(bytes)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	i := -1
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// formatSnapshotTSHuman turns a snapshot TS like "2026-11-16T0300-00"
// into the "YYYY-MM-DD HH:MM" shape the list-without-entry view wants.
// Falls back to the raw TS when the layout does not parse — never a
// value worth hiding from the operator. Mirrors cmd/devm's
// formatTSHuman.
func formatSnapshotTSHuman(ts string) string {
	if ts == "" {
		return "<never>"
	}
	t, err := time.Parse("2006-01-02T1504-05", ts)
	if err != nil {
		return ts
	}
	return t.Format("2006-01-02 15:04")
}
