package serviceapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"google.golang.org/grpc"

	"github.com/mdubb86/devm/internal/ironproxy/transformv1"
	"github.com/mdubb86/devm/internal/policymatch"
)

// Mode is the per-project egress response variant. Passthrough lets
// every request through (iron-proxy still MITMs and substitutes secrets);
// Restricted consults the project's allowlist for each request. The two
// modes flip freely mid-life without touching softnet or iron-proxy.
type Mode int

const (
	// ModeRestricted (zero value) is the safe default — a project that
	// has never had SetMode called sees allowlist-gated egress.
	ModeRestricted Mode = iota
	ModePassthrough
)

func (m Mode) String() string {
	switch m {
	case ModePassthrough:
		return "passthrough"
	case ModeRestricted:
		return "restricted"
	default:
		return fmt.Sprintf("Mode(%d)", int(m))
	}
}

// policyAuthority is the daemon-wide egress policy authority behind
// every project's iron-proxy grpc transform. Package-level for the same
// reason as ironProxyState: it is daemon-lifetime state shared by the
// cold-start, live-apply, and adoption paths.
var policyAuthority = NewPolicyAuthority()

// PolicyAuthority serves iron-proxy's TransformService on one unix
// socket per project and answers allow/deny per request from the
// project's current network.allow list. It is the single place the
// egress decision is made (matching semantics: internal/policymatch);
// iron-proxy consults it on every proxied request via the grpc
// transform emitted by IronProxyConfig.YAML().
//
// A REJECT carries a devm-authored response — status 403 with an
// X-Devm-Blocked header and a JSON body naming the blocked request —
// which iron-proxy delivers to the guest client verbatim (pinned by
// e2e/test_iron_contract_09_grpc_transform_custom_reject.py). This is
// what lets a guest tell a policy block from a genuine upstream 403.
//
// Projects with no Set() allowlist deny everything: a socket that is
// serving but unconfigured must fail closed, and doing it here (rather
// than letting iron-proxy 502 on a missing socket) keeps the reject
// self-describing.
type PolicyAuthority struct {
	mu        sync.Mutex
	allow     map[string][]string
	modes     map[string]Mode
	listeners map[string]*policyListener
}

type policyListener struct {
	sock   string
	server *grpc.Server
}

// NewPolicyAuthority returns an empty authority. Production uses the
// package-level policyAuthority; tests construct their own.
func NewPolicyAuthority() *PolicyAuthority {
	return &PolicyAuthority{
		allow:     map[string][]string{},
		modes:     map[string]Mode{},
		listeners: map[string]*policyListener{},
	}
}

// Set replaces projectID's allowlist. Takes effect on the next request —
// no re-serve, no iron-proxy respawn.
func (p *PolicyAuthority) Set(projectID string, allow []string) {
	cp := append([]string{}, allow...)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allow[projectID] = cp
}

// SetMode replaces projectID's egress mode. Takes effect on the next
// request — no re-serve, no iron-proxy respawn. Safe to call before
// EnsureServing.
func (p *PolicyAuthority) SetMode(projectID string, mode Mode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.modes[projectID] = mode
}

// modeFor returns projectID's current mode, or ModeRestricted for a
// project that has never had SetMode called.
func (p *PolicyAuthority) modeFor(projectID string) Mode {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.modes[projectID]
}

// EnsureServing binds projectID's TransformService on sock and starts
// serving. Idempotent when already serving on the same path; a changed
// path stops the old listener first. A stale socket file (dead daemon's
// leftover) is removed before binding.
func (p *PolicyAuthority) EnsureServing(projectID, sock string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.listeners[projectID]; ok {
		if l.sock == sock {
			return nil
		}
		l.server.Stop()
		_ = os.Remove(l.sock)
		delete(p.listeners, projectID)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0700); err != nil {
		return fmt.Errorf("policy socket %s: %w", sock, err)
	}
	_ = os.Remove(sock)
	lis, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("policy socket %s: %w", sock, err)
	}
	server := grpc.NewServer()
	transformv1.RegisterTransformServiceServer(server, &policyService{authority: p, projectID: projectID})
	p.listeners[projectID] = &policyListener{sock: sock, server: server}
	go func() {
		// Serve returns on server.Stop(); anything else means the
		// listener died and egress for this project will 502 fail-closed.
		if err := server.Serve(lis); err != nil && err != grpc.ErrServerStopped {
			log.Printf("policy: TransformService for %s exited: %v", projectID, err)
		}
	}()
	log.Printf("policy: serving egress policy for %s on %s", projectID, sock)
	return nil
}

// StopServing stops projectID's listener and removes its socket file.
// The allowlist entry is dropped too — a stopped project's policy is
// re-Set on its next start.
func (p *PolicyAuthority) StopServing(projectID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.listeners[projectID]; ok {
		l.server.Stop()
		_ = os.Remove(l.sock)
		delete(p.listeners, projectID)
	}
	delete(p.allow, projectID)
	delete(p.modes, projectID)
}

func (p *PolicyAuthority) allowlistFor(projectID string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allow[projectID]
}

// policyService implements transformv1.TransformServiceServer for one
// project.
type policyService struct {
	transformv1.UnimplementedTransformServiceServer
	authority *PolicyAuthority
	projectID string
}

func (s *policyService) TransformRequest(ctx context.Context, req *transformv1.TransformRequestRequest) (*transformv1.TransformRequestResponse, error) {
	if s.authority.modeFor(s.projectID) == ModePassthrough {
		return &transformv1.TransformRequestResponse{
			Action: transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE,
		}, nil
	}
	r := req.GetRequest()
	host := policymatch.StripPort(r.GetHost())
	// r.Url is path+query for proxied requests and empty for CONNECT.
	// Match on the path alone — query strings never participate
	// (schema.Network.validate rejects '?' in patterns for the same
	// reason).
	reqPath := ""
	if u, err := url.Parse(r.GetUrl()); err == nil {
		reqPath = u.Path
	}
	if policymatch.Allowed(s.authority.allowlistFor(s.projectID), host, reqPath) {
		return &transformv1.TransformRequestResponse{
			Action: transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE,
		}, nil
	}
	body, _ := json.Marshal(map[string]string{
		"blocked_by": "devm-egress-policy",
		"host":       host,
		"method":     r.GetMethod(),
		"url":        r.GetUrl(),
		"hint":       "add the host to network.allow in devm.yaml, or open a window with `devm passthrough`",
	})
	return &transformv1.TransformRequestResponse{
		Action: transformv1.TransformAction_TRANSFORM_ACTION_REJECT,
		Response: &transformv1.HttpResponse{
			StatusCode: http.StatusForbidden,
			Headers: map[string]*transformv1.HeaderValues{
				"X-Devm-Blocked": {Values: []string{"egress-policy"}},
				"Content-Type":   {Values: []string{"application/json"}},
			},
			Body: append(body, '\n'),
		},
	}, nil
}

func (s *policyService) TransformResponse(ctx context.Context, req *transformv1.TransformResponseRequest) (*transformv1.TransformResponseResponse, error) {
	return &transformv1.TransformResponseResponse{
		Action: transformv1.TransformAction_TRANSFORM_ACTION_CONTINUE,
	}, nil
}
