package serviceapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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

	pa.Set("projA", []string{"example.com", "*.github.com", "httpbin.org/get*"})
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

func TestPolicyAuthorityLiveUpdateAndRestart(t *testing.T) {
	pa := NewPolicyAuthority()
	t.Cleanup(func() { pa.StopServing("projB") })

	dir, err := os.MkdirTemp("", "pol")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "p.sock")

	// No Set() yet → unknown project denies (fail-closed at the policy
	// layer, but with devm's explanatory 403, not iron-proxy's 502).
	require.NoError(t, pa.EnsureServing("projB", sock))
	client := dialPolicy(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.TransformRequest(ctx, policyReq("example.com", "GET", "/"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_REJECT, resp.GetAction())

	// Set() takes effect on the next request — no re-serve needed.
	pa.Set("projB", []string{"example.com"})
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
	pa.Set("projB", []string{"example.com"})
	client2 := dialPolicy(t, sock)
	resp, err = client2.TransformRequest(ctx, policyReq("example.com", "GET", "/"))
	require.NoError(t, err)
	require.Equal(t, transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE, resp.GetAction())
}
