package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeApproveDaemon returns a test server that serves canned
// GET /vm/approve-state and records POST /vm/approve calls.
type fakeApproveDaemon struct {
	*httptest.Server
	stateBody   []byte
	approveHits int
}

func newFakeApproveDaemon(stateBody []byte) *fakeApproveDaemon {
	f := &fakeApproveDaemon{stateBody: stateBody}
	mux := http.NewServeMux()
	mux.HandleFunc("/vm/approve-state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(f.stateBody)
	})
	mux.HandleFunc("/vm/approve", func(w http.ResponseWriter, r *http.Request) {
		f.approveHits++
		w.WriteHeader(http.StatusNoContent)
	})
	f.Server = httptest.NewServer(mux)
	return f
}

// ttyYes is the TTY probe tests use when they want the interactive
// code path. The real gate is pinned by TestApprove_NonTTYRefuses.
func ttyYes() bool { return true }

func TestApprove_UnchangedPrintsAlreadyApprovedAndExits(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"project":  "p",
		"diverged": false,
	})
	f := newFakeApproveDaemon(body)
	defer f.Close()
	var stdout, stderr bytes.Buffer
	err := runApprove(approveOpts{
		daemonURL:  f.URL,
		httpClient: f.Client(),
		projectID:  "p",
		stdin:      strings.NewReader(""),
		stdout:     &stdout,
		stderr:     &stderr,
		isTTY:      ttyYes,
	})
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "already approved")
	assert.Equal(t, 0, f.approveHits, "no POST /vm/approve when unchanged")
}

func TestApprove_DivergedYesAdvancesSnapshot(t *testing.T) {
	cur := base64.StdEncoding.EncodeToString([]byte("project:\n  name: p2\n"))
	prev := base64.StdEncoding.EncodeToString([]byte("project:\n  name: p1\n"))
	body, _ := json.Marshal(map[string]any{
		"project":             "p",
		"diverged":            true,
		"current_devm_bytes":  cur,
		"approved_devm_bytes": prev,
		"current_me_bytes":    nil,
		"approved_me_bytes":   nil,
	})
	f := newFakeApproveDaemon(body)
	defer f.Close()
	var stdout, stderr bytes.Buffer
	err := runApprove(approveOpts{
		daemonURL:  f.URL,
		httpClient: f.Client(),
		projectID:  "p",
		stdin:      strings.NewReader("y\n"),
		stdout:     &stdout,
		stderr:     &stderr,
		isTTY:      ttyYes,
	})
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "devm.yaml")
	assert.Contains(t, stdout.String(), "approved")
	assert.Equal(t, 1, f.approveHits)
}

func TestApprove_DivergedNoDoesNotAdvance(t *testing.T) {
	cur := base64.StdEncoding.EncodeToString([]byte("project:\n  name: p2\n"))
	prev := base64.StdEncoding.EncodeToString([]byte("project:\n  name: p1\n"))
	body, _ := json.Marshal(map[string]any{
		"project": "p", "diverged": true,
		"current_devm_bytes": cur, "approved_devm_bytes": prev,
	})
	f := newFakeApproveDaemon(body)
	defer f.Close()
	var stdout, stderr bytes.Buffer
	err := runApprove(approveOpts{
		daemonURL: f.URL, httpClient: f.Client(), projectID: "p",
		stdin: strings.NewReader("n\n"), stdout: &stdout, stderr: &stderr,
		isTTY: ttyYes,
	})
	assert.Error(t, err, "N answer must exit non-zero")
	assert.Equal(t, 0, f.approveHits)
}

func TestApprove_DivergedEOFDoesNotAdvance(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"project": "p", "diverged": true, "current_devm_bytes": "", "approved_devm_bytes": ""})
	f := newFakeApproveDaemon(body)
	defer f.Close()
	var stdout, stderr bytes.Buffer
	err := runApprove(approveOpts{
		daemonURL: f.URL, httpClient: f.Client(), projectID: "p",
		stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr,
		isTTY: ttyYes,
	})
	assert.Error(t, err, "EOF answer must exit non-zero")
	assert.Equal(t, 0, f.approveHits)
}

// TestApprove_NonTTYRefuses pins the human-presence contract: when
// stdin is piped (an agent, a script), approve refuses before any
// POST /vm/approve. The gate exists specifically so an agent with
// shell access cannot self-approve its own config edits — pre-fix
// `echo y | devm approve` succeeded.
func TestApprove_NonTTYRefuses(t *testing.T) {
	cur := base64.StdEncoding.EncodeToString([]byte("project:\n  name: p2\n"))
	prev := base64.StdEncoding.EncodeToString([]byte("project:\n  name: p1\n"))
	body, _ := json.Marshal(map[string]any{
		"project": "p", "diverged": true,
		"current_devm_bytes": cur, "approved_devm_bytes": prev,
	})
	f := newFakeApproveDaemon(body)
	defer f.Close()
	var stdout, stderr bytes.Buffer
	err := runApprove(approveOpts{
		daemonURL: f.URL, httpClient: f.Client(), projectID: "p",
		stdin: strings.NewReader("y\n"), stdout: &stdout, stderr: &stderr,
		isTTY: func() bool { return false },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a terminal")
	assert.Equal(t, 0, f.approveHits, "non-TTY must never POST /vm/approve, even with 'y' on stdin")
	assert.Contains(t, stdout.String(), "devm.yaml", "diff must still print so an operator can see what the agent tried to approve")
}

// TestApprove_NilIsTTYDefaultsToRefuse pins the fail-closed default:
// production sets isTTY explicitly, but any direct runApprove caller
// that leaves it nil (e.g. a future handler written without reading
// the comment) still gets the refusal rather than silent approval.
func TestApprove_NilIsTTYDefaultsToRefuse(t *testing.T) {
	cur := base64.StdEncoding.EncodeToString([]byte("project:\n  name: p2\n"))
	prev := base64.StdEncoding.EncodeToString([]byte("project:\n  name: p1\n"))
	body, _ := json.Marshal(map[string]any{
		"project": "p", "diverged": true,
		"current_devm_bytes": cur, "approved_devm_bytes": prev,
	})
	f := newFakeApproveDaemon(body)
	defer f.Close()
	var stdout, stderr bytes.Buffer
	err := runApprove(approveOpts{
		daemonURL: f.URL, httpClient: f.Client(), projectID: "p",
		stdin: strings.NewReader("y\n"), stdout: &stdout, stderr: &stderr,
		// isTTY left nil
	})
	require.Error(t, err)
	assert.Equal(t, 0, f.approveHits)
}

func TestApprove_OldDaemonReturns404_SurfacesUpgradeHint(t *testing.T) {
	// A pre-approve-gate daemon build 404s any /vm/* route.
	// runApprove must surface the upgrade hint.
	mux := http.NewServeMux()
	mux.HandleFunc("/vm/approve-state", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	var stdout, stderr bytes.Buffer
	err := runApprove(approveOpts{
		daemonURL: srv.URL, projectID: "p",
		stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon does not support approve gate")
}
