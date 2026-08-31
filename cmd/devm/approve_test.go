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
		macCwd:     "/tmp/x",
		stdin:      strings.NewReader(""),
		stdout:     &stdout,
		stderr:     &stderr,
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
		macCwd:     "/tmp/x",
		stdin:      strings.NewReader("y\n"),
		stdout:     &stdout,
		stderr:     &stderr,
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
		daemonURL: f.URL, httpClient: f.Client(), projectID: "p", macCwd: "/tmp/x",
		stdin: strings.NewReader("n\n"), stdout: &stdout, stderr: &stderr,
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
		daemonURL: f.URL, httpClient: f.Client(), projectID: "p", macCwd: "/tmp/x",
		stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr,
	})
	assert.Error(t, err, "EOF answer must exit non-zero")
	assert.Equal(t, 0, f.approveHits)
}
