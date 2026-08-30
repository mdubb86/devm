package serviceapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	transformv1 "github.com/mdubb86/devm/internal/ironproxy/transformv1"
)

// dialPolicy connects a TransformService client to a unix socket.
func dialPolicy(t *testing.T, sock string) transformv1.TransformServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(
		"unix://"+sock,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return transformv1.NewTransformServiceClient(conn)
}

func policyReq(host, method, url string) *transformv1.TransformRequestRequest {
	return &transformv1.TransformRequestRequest{
		Request: &transformv1.HttpRequest{
			Method: method,
			Url:    url,
			Host:   host,
		},
	}
}

func TestPolicyAuthorityAllowAndReject(t *testing.T) {
	pa := NewPolicyAuthority()
	t.Cleanup(func() { pa.StopServing("projA") })

	// os.MkdirTemp (not t.TempDir): macOS caps sun_path at 104 bytes and
	// t.TempDir paths grow with the test name.
	dir, err := os.MkdirTemp("", "pol")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")

	pa.SetAllowlist("projA", []string{"example.com", "*.github.com", "httpbin.org/get*"})
	require.NoError(t, pa.EnsureServing("projA", sock))

	client := dialPolicy(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Allowed host → CONTINUE, no response payload.
	resp, err := client.TransformRequest(ctx, policyReq("example.com", "GET", "/anything"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())
	require.Nil(t, resp.GetResponse())

	// Wildcard subdomain → CONTINUE. Port must be stripped before matching.
	resp, err = client.TransformRequest(ctx, policyReq("api.github.com:443", "CONNECT", ""))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())

	// Path-scoped entry: matching path continues, other path rejects.
	resp, err = client.TransformRequest(ctx, policyReq("httpbin.org", "GET", "/get"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())

	resp, err = client.TransformRequest(ctx, policyReq("httpbin.org", "POST", "/post"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_REJECT, resp.GetAction())

	// Denied host → REJECT with the devm-authored response, verbatim.
	resp, err = client.TransformRequest(ctx, policyReq("blocked.example", "GET", "/x?q=1"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_REJECT, resp.GetAction())
	r := resp.GetResponse()
	require.NotNil(t, r)
	require.Equal(t, int32(http.StatusForbidden), r.GetStatusCode())
	require.Equal(t, []string{"egress-policy"}, r.GetHeaders()["X-Devm-Blocked"].GetValues())
	require.Equal(t, []string{"application/json"}, r.GetHeaders()["Content-Type"].GetValues())
	var body struct {
		BlockedBy string `json:"blocked_by"`
		Host      string `json:"host"`
		Method    string `json:"method"`
		URL       string `json:"url"`
		Hint      string `json:"hint"`
	}
	require.NoError(t, json.Unmarshal(r.GetBody(), &body))
	require.Equal(t, "devm-egress-policy", body.BlockedBy)
	require.Equal(t, "blocked.example", body.Host)
	require.Equal(t, "GET", body.Method)
	require.Equal(t, "/x?q=1", body.URL)
	require.NotEmpty(t, body.Hint)
}

// TestPolicyAuthorityEnsureServingCreatesSocketDir pins the fresh-state-dir
// cold start: EnsureServing must create the socket's parent directory
// itself rather than relying on a caller (writeIronProxyConfig's MkdirAll
// in SpawnIronProxy runs AFTER EnsureServing, so on a brand-new state dir
// the iron-proxy/ subdirectory does not exist yet when we bind).
func TestPolicyAuthorityEnsureServingCreatesSocketDir(t *testing.T) {
	pa := NewPolicyAuthority()
	t.Cleanup(func() { pa.StopServing("projC") })

	dir, err := os.MkdirTemp("", "pol")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// iron-proxy/ does not exist yet — EnsureServing must create it.
	sock := filepath.Join(dir, "iron-proxy", "p.sock")

	require.NoError(t, pa.EnsureServing("projC", sock))

	pa.SetAllowlist("projC", []string{"example.com"})
	client := dialPolicy(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.TransformRequest(ctx, policyReq("example.com", "GET", "/"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())
}

func TestPolicyAuthorityLiveUpdateAndRestart(t *testing.T) {
	pa := NewPolicyAuthority()
	t.Cleanup(func() { pa.StopServing("projB") })

	dir, err := os.MkdirTemp("", "pol")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")

	// No SetAllowlist() yet → unknown project denies (fail-closed at the policy
	// layer, but with devm's explanatory 403, not iron-proxy's 502).
	require.NoError(t, pa.EnsureServing("projB", sock))
	client := dialPolicy(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.TransformRequest(ctx, policyReq("example.com", "GET", "/"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_REJECT, resp.GetAction())

	// SetAllowlist() takes effect on the next request — no re-serve needed.
	pa.SetAllowlist("projB", []string{"example.com"})
	resp, err = client.TransformRequest(ctx, policyReq("example.com", "GET", "/"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())

	// EnsureServing is idempotent for the same socket path.
	require.NoError(t, pa.EnsureServing("projB", sock))
	resp, err = client.TransformRequest(ctx, policyReq("example.com", "GET", "/"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())

	// A stale socket file from a dead daemon must not block re-serving.
	pa.StopServing("projB")
	require.NoError(t, os.WriteFile(sock, nil, 0o600))
	require.NoError(t, pa.EnsureServing("projB", sock))
	pa.SetAllowlist("projB", []string{"example.com"})
	client2 := dialPolicy(t, sock)
	resp, err = client2.TransformRequest(ctx, policyReq("example.com", "GET", "/"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())
}

func TestPolicyAuthority_PassthroughShortCircuits(t *testing.T) {
	p := NewPolicyAuthority()
	// Set a restrictive allowlist that would reject everything.
	p.SetAllowlist("proj1", nil)
	p.SetMode("proj1", ModePassthrough)

	svc := &policyService{authority: p, projectID: "proj1"}
	req := &transformv1.TransformRequestRequest{
		Request: &transformv1.HttpRequest{
			Host:   "example.com",
			Url:    "/blocked",
			Method: "GET",
		},
	}
	resp, err := svc.TransformRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	if resp.GetAction() != transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE {
		t.Fatalf("passthrough mode should short-circuit to CONTINUE, got %v", resp.GetAction())
	}
}

func TestPolicyAuthority_RestrictedConsultsAllowlist(t *testing.T) {
	p := NewPolicyAuthority()
	p.SetAllowlist("proj1", []string{"allowed.example.com"})
	p.SetMode("proj1", ModeRestricted)

	svc := &policyService{authority: p, projectID: "proj1"}
	req := &transformv1.TransformRequestRequest{
		Request: &transformv1.HttpRequest{
			Host: "blocked.example.com",
			Url:  "/",
		},
	}
	resp, err := svc.TransformRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}
	if resp.GetAction() != transformv1.TransformAction_TRANSFORM_ACTION_REJECT {
		t.Fatalf("restricted mode should reject unlisted host, got %v", resp.GetAction())
	}
}

func TestPolicyAuthority_DefaultsToRestricted(t *testing.T) {
	p := NewPolicyAuthority()
	p.SetAllowlist("proj1", []string{"allowed.example.com"})
	// No SetMode call — should default to restricted.

	svc := &policyService{authority: p, projectID: "proj1"}
	req := &transformv1.TransformRequestRequest{
		Request: &transformv1.HttpRequest{Host: "blocked.example.com", Url: "/"},
	}
	resp, _ := svc.TransformRequest(context.Background(), req)
	if resp.GetAction() != transformv1.TransformAction_TRANSFORM_ACTION_REJECT {
		t.Fatalf("default mode should be restricted (reject), got %v", resp.GetAction())
	}
}

// A policy reject lands in the authority's own counts at the decision
// point; an allow and a passthrough-mode request record nothing.
func TestPolicyAuthorityRecordsDenials(t *testing.T) {
	pa := NewPolicyAuthority()
	t.Cleanup(func() { pa.StopServing("projD") })

	dir, err := os.MkdirTemp("", "pol")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")
	pa.SetAllowlist("projD", []string{"example.com"})
	require.NoError(t, pa.EnsureServing("projD", sock))

	client := dialPolicy(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for range 2 {
		resp, err := client.TransformRequest(ctx, policyReq("blocked.example:443", "GET", "/x?q=1"))
		require.NoError(t, err)
		require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_REJECT, resp.GetAction())
	}
	resp, err := client.TransformRequest(ctx, policyReq("example.com", "GET", "/ok"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())

	pa.SetMode("projD", ModePassthrough)
	resp, err = client.TransformRequest(ctx, policyReq("blocked.example", "GET", "/y"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())
	pa.SetMode("projD", ModeRestricted)

	got := pa.SnapshotDenials("projD")
	require.Len(t, got, 1)
	require.Equal(t, "blocked.example", got[0].Host, "recorded port-stripped")
	require.Equal(t, "/x", got[0].Path, "recorded query-stripped")
	require.Equal(t, "GET", got[0].Method)
	require.Equal(t, 2, got[0].Count)
}

// SetAllowlist replays every stored row through policymatch: rows the new
// list would allow are deleted, still-blocked rows are kept. One rule —
// unchanged lists keep everything, removals keep everything.
func TestPolicyAuthoritySetAllowlistReplaysRows(t *testing.T) {
	pa := NewPolicyAuthority()
	t.Cleanup(func() { pa.StopServing("projE") })

	dir, err := os.MkdirTemp("", "pol")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")
	pa.SetAllowlist("projE", []string{"allowed.example"})
	require.NoError(t, pa.EnsureServing("projE", sock))

	client := dialPolicy(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deny := func(host, path string) {
		t.Helper()
		resp, err := client.TransformRequest(ctx, policyReq(host, "GET", path))
		require.NoError(t, err)
		require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_REJECT, resp.GetAction())
	}
	deny("a.example", "/one")
	deny("b.example", "/two")
	deny("api.svc.example", "/v1/models")
	deny("api.svc.example", "/admin")
	require.Len(t, pa.SnapshotDenials("projE"), 4)

	// Unchanged list → everything kept.
	pa.SetAllowlist("projE", []string{"allowed.example"})
	require.Len(t, pa.SnapshotDenials("projE"), 4)

	// Add a.example (exact) and a path-scoped entry: the a.example row and
	// the /v1 row resolve; b.example and /admin stay.
	pa.SetAllowlist("projE", []string{"allowed.example", "a.example", "api.svc.example/v1/*"})
	got := pa.SnapshotDenials("projE")
	require.Len(t, got, 2)
	hosts := map[string]string{}
	for _, d := range got {
		hosts[d.Host+d.Path] = d.Path
	}
	require.Contains(t, hosts, "b.example/two")
	require.Contains(t, hosts, "api.svc.example/admin")

	// Removing entries can never resolve a row → everything kept.
	pa.SetAllowlist("projE", []string{"allowed.example"})
	require.Len(t, pa.SnapshotDenials("projE"), 2)
}

// VM stop preserves policy state and counts; teardown purges everything.
func TestPolicyAuthorityStopPreservesTeardownPurges(t *testing.T) {
	pa := NewPolicyAuthority()
	t.Cleanup(func() { pa.PurgeProject("projF") })

	dir, err := os.MkdirTemp("", "pol")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")
	pa.SetAllowlist("projF", []string{"a.example"})
	require.NoError(t, pa.EnsureServing("projF", sock))
	client := dialPolicy(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.TransformRequest(ctx, policyReq("blocked.example", "GET", "/x"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_REJECT, resp.GetAction())

	pa.StopServing("projF")
	require.Len(t, pa.SnapshotDenials("projF"), 1)
	pa.SetAllowlist("projF", []string{"a.example"})
	require.Len(t, pa.SnapshotDenials("projF"), 1, "stop + same-list restart preserves")

	pa.PurgeProject("projF")
	require.Empty(t, pa.SnapshotDenials("projF"))
}

func TestPolicyAuthority_StopServingDropsMode(t *testing.T) {
	p := NewPolicyAuthority()
	p.SetMode("proj1", ModePassthrough)
	p.StopServing("proj1")

	// After stop, a fresh SetMode should not carry over the old mode state.
	// Verify by re-checking the internal accessor.
	if got := p.modeFor("proj1"); got != ModeRestricted {
		t.Fatalf("StopServing should drop mode; modeFor after stop = %v, want restricted", got)
	}
}

// A denial decided just before an allowlist edit must never surface as a
// row for a now-allowed host after the edit's replay: the decision and
// its counter row are one atomic step under the authority's lock, so an
// edit either sweeps the row (decision completed first) or the decision
// sees the new list (edit completed first). Hammers the real service
// path in-process while flipping the allowlist to maximize interleaving.
func TestPolicyAuthorityDecisionAndRecordAreAtomic(t *testing.T) {
	pa := NewPolicyAuthority()
	const proj = "projRace"
	t.Cleanup(func() { pa.PurgeProject(proj) })
	pa.SetAllowlist(proj, []string{"allowed.example"})

	svc := &policyService{authority: pa, projectID: proj}
	ctx := context.Background()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = svc.TransformRequest(ctx, policyReq("x.example", "GET", "/p"))
			}
		}()
	}
	defer func() { close(stop); wg.Wait() }()

	for i := range 1000 {
		pa.SetAllowlist(proj, []string{"allowed.example", "x.example"})
		for _, d := range pa.SnapshotDenials(proj) {
			if d.Host == "x.example" {
				t.Fatalf("iteration %d: row for now-allowed host survived the edit's replay: %+v", i, d)
			}
		}
		pa.SetAllowlist(proj, []string{"allowed.example"})
	}
}
