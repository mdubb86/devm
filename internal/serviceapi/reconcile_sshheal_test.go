package serviceapi

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/approve"
	"github.com/mdubb86/devm/internal/identity"
	"github.com/mdubb86/devm/internal/reconcile"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/supervisor"
)

// postReconcile drives the handler with a running VM named p and
// returns the decoded response.
func postReconcile(t *testing.T, req VMReconcileRequest) VMReconcileResponse {
	t.Helper()
	// Ensure WorkspaceHostPath is set for the approve gate check.
	if req.WorkspaceHostPath == "" {
		projDir := t.TempDir()
		// Create a simple devm.yaml matching the cfg.
		devmYAML := "project:\n  name: " + req.Cfg.Project.Name + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(projDir, "devm.yaml"), []byte(devmYAML), 0644))
		// Approve the snapshot so the gate check passes.
		store := approve.NewStore(identity.Prod)
		require.NoError(t, store.Write(req.Name, []byte(devmYAML), nil, "user"))
		req.WorkspaceHostPath = projDir
	}
	body, _ := json.Marshal(req)
	server := NewServer(identity.Prod.SocketPath(), Build{})
	RegisterReconcileHandler(server, identity.Prod, NewProjectLocks(), &fakeApply{}, &fakePackages{}, &fakeTartList{running: true, vmName: req.Name}, supervisor.New(t.TempDir()), nil, 0)
	rec := httptest.NewRecorder()
	server.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/vm/reconcile", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp VMReconcileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

// TestVMReconcile_CrossWiredSSH_HealsToFreshIP pins the heal: when the
// project's :22 answers with a foreign host key, reconcile reallocates
// the ProjectIP, re-pushes ingress, reports a synthetic
// KindSSHEndpointHealed change, and the snapshot mirrors the new IP.
func TestVMReconcile_CrossWiredSSH_HealsToFreshIP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetIronProxyState(t)
	// The squatter is an SSH server holding .1:22, so the allocator's
	// probe sees the address as in-use and the heal lands elsewhere —
	// same coupling as production.
	probeIPInUse = func(ip string) bool { return ip == "127.42.0.1" }

	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "p", StateSnapshot{Cfg: cfg, ProjectIP: "127.42.0.1"}))
	ironProxyState.put("p", projectInfo{ProjectIP: "127.42.0.1"})
	t.Cleanup(func() { ironProxyState.del("p") })
	registerFakeSoftnet(t, "p")

	wrongSigner, _ := genSigner(t)
	rightSigner, rightPub := genSigner(t)
	wrongAddr := fakeSSHD(t, wrongSigner) // squatter answering the old IP
	rightAddr := fakeSSHD(t, rightSigner) // the project's own sshd on any healed IP
	origAddr := sshVerifyAddr
	sshVerifyAddr = func(ip string) string {
		if ip == "127.42.0.1" {
			return wrongAddr
		}
		return rightAddr
	}
	t.Cleanup(func() { sshVerifyAddr = origAddr })

	resp := postReconcile(t, VMReconcileRequest{Name: "p", Cfg: cfg, SSHHostPub: rightPub})

	require.Len(t, resp.Applied, 1)
	healed := resp.Applied[0]
	assert.Equal(t, reconcile.KindSSHEndpointHealed, healed.Kind)
	assert.Equal(t, "127.42.0.1", healed.Old)
	assert.NotEqual(t, healed.Old, healed.New)

	info, ok := ironProxyState.get("p")
	require.True(t, ok)
	assert.Equal(t, healed.New, info.ProjectIP, "in-memory state must carry the healed IP")
	snap, err := ReadStateSnapshot(identity.Prod, "p")
	require.NoError(t, err)
	assert.Equal(t, healed.New, snap.ProjectIP, "snapshot must mirror the healed IP")
}

// TestVMReconcile_HealthySSH_NoHeal pins the quiet path: matching host
// key → no synthetic change, IP untouched.
func TestVMReconcile_HealthySSH_NoHeal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetIronProxyState(t)

	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "p", StateSnapshot{Cfg: cfg, ProjectIP: "127.42.0.1"}))
	ironProxyState.put("p", projectInfo{ProjectIP: "127.42.0.1"})
	t.Cleanup(func() { ironProxyState.del("p") })

	signer, pub := genSigner(t)
	addr := fakeSSHD(t, signer)
	origAddr := sshVerifyAddr
	sshVerifyAddr = func(string) string { return addr }
	t.Cleanup(func() { sshVerifyAddr = origAddr })

	resp := postReconcile(t, VMReconcileRequest{Name: "p", Cfg: cfg, SSHHostPub: pub})
	assert.Empty(t, resp.Applied)
	info, _ := ironProxyState.get("p")
	assert.Equal(t, "127.42.0.1", info.ProjectIP)
}

// TestVMReconcile_InconclusiveSSH_NoHeal pins the guard: a listener
// that accepts TCP but never speaks SSH (booting guest, wedged sshd,
// raw squatter) must NOT trigger a reallocation — only a definitive
// key mismatch does.
func TestVMReconcile_InconclusiveSSH_NoHeal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetIronProxyState(t)

	cfg := schema.Config{Project: schema.Project{Name: "p"}}
	require.NoError(t, WriteStateSnapshot(identity.Prod, "p", StateSnapshot{Cfg: cfg, ProjectIP: "127.42.0.1"}))
	ironProxyState.put("p", projectInfo{ProjectIP: "127.42.0.1"})
	t.Cleanup(func() { ironProxyState.del("p") })

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer raw.Close()
	_, pub := genSigner(t)
	origAddr := sshVerifyAddr
	sshVerifyAddr = func(string) string { return raw.Addr().String() }
	t.Cleanup(func() { sshVerifyAddr = origAddr })

	resp := postReconcile(t, VMReconcileRequest{Name: "p", Cfg: cfg, SSHHostPub: pub})
	assert.Empty(t, resp.Applied, "inconclusive probe must not heal")
	info, _ := ironProxyState.get("p")
	assert.Equal(t, "127.42.0.1", info.ProjectIP)
}
