package serviceapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/mdubb86/devm/internal/daemonlog"
	"github.com/mdubb86/devm/internal/ironproxy/transformv1"
	"github.com/mdubb86/devm/internal/policymatch"
	"github.com/mdubb86/devm/internal/secret"
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
// Projects with no SetAllowlist() allowlist deny everything: a socket
// that is serving but unconfigured must fail closed, and doing it here
// (rather than letting iron-proxy 502 on a missing socket) keeps the
// reject self-describing.
//
// The authority also owns denial counts: every REJECT it hands out is
// recorded in denials, and SetAllowlist replays those rows through the
// new list so `devm denials` never shows a host the current allowlist
// would now let through.
type PolicyAuthority struct {
	mu        sync.Mutex
	allow     map[string][]string
	modes     map[string]Mode
	listeners map[string]*policyListener
	denials   *Denials
	secrets   secret.Backend
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
		denials:   NewDenials(),
	}
}

// SetAllowlist replaces projectID's allowlist. Takes effect on the next
// request — no re-serve, no iron-proxy respawn.
//
// Every previously-recorded denial row for projectID is replayed against
// the new list: a row the new list now allows is deleted, so `devm
// denials` never shows a host that would pass today. Rows still blocked
// by the new list — and rows blocked by a narrower list after entries
// were removed — are left alone; a removal can never resolve a row.
func (p *PolicyAuthority) SetAllowlist(projectID string, allow []string) {
	cp := append([]string{}, allow...)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allow[projectID] = cp
	p.denials.invalidateResolved(projectID, func(host, path string) bool {
		return policymatch.Allowed(cp, host, path)
	})
}

// SnapshotDenials returns projectID's current denial counts, most-denied
// first. Empty slice when the project has no denials.
func (p *PolicyAuthority) SnapshotDenials(projectID string) []Denial {
	return p.denials.Snapshot(projectID)
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
// Stop is a listener event, not a policy reset: the allowlist and its
// denial counts persist so the next EnsureServing/SetAllowlist picks up
// where the project left off. modes is still dropped — mode
// non-persistence across a stop is the always-through-iron-proxy
// feature's contract, independent of policy state. Call PurgeProject
// instead when the project itself is going away (teardown).
func (p *PolicyAuthority) StopServing(projectID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.listeners[projectID]; ok {
		l.server.Stop()
		_ = os.Remove(l.sock)
		delete(p.listeners, projectID)
	}
	delete(p.modes, projectID)
}

// PurgeProject tears down projectID's listener (if any) and deletes
// every trace of its policy state: allowlist, mode, and denial counts.
// Call this on teardown/destroy, when the project itself is gone and a
// future project reusing the name must not inherit stale counts. Use
// StopServing instead for a plain stop, which preserves state for the
// next start.
func (p *PolicyAuthority) PurgeProject(projectID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.listeners[projectID]; ok {
		l.server.Stop()
		_ = os.Remove(l.sock)
		delete(p.listeners, projectID)
	}
	delete(p.allow, projectID)
	delete(p.modes, projectID)
	p.denials.clearProject(projectID)
}

// UseSecretBackend sets or replaces the backend the egress gate reads
// secret values from. Called once at daemon startup from RunService;
// tests exercising the gate call it after NewPolicyAuthority. A nil
// backend disables the gate (unwired state).
func (p *PolicyAuthority) UseSecretBackend(b secret.Backend) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.secrets = b
}

// secretBackend returns the currently wired backend, or nil when
// unwired. Private so callers go through decide() / the gate check.
func (p *PolicyAuthority) secretBackend() secret.Backend {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.secrets
}

// allowlistFor returns projectID's current allowlist — an observation
// accessor for tests; the request path decides via decide, never this.
func (p *PolicyAuthority) allowlistFor(projectID string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allow[projectID]
}

// decide is the egress decision point: under a single lock hold it reads
// the project's mode and allowlist, evaluates the request, and — when
// rejecting — records the denial before releasing. An allowlist edit can
// therefore never interleave between a decision and its counter row: the
// row either exists when SetAllowlist's replay runs, or the decision runs
// after the edit and sees the new list. Recording nests the tracker's
// lock inside p.mu — the same order SetAllowlist's replay uses.
func (p *PolicyAuthority) decide(projectID, host, path, method string) (allowed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.modes[projectID] == ModePassthrough {
		return true
	}
	if policymatch.Allowed(p.allow[projectID], host, path) {
		return true
	}
	p.denials.Record(projectID, host, path, method, time.Now().UTC())
	return false
}

// Secret-placeholder tokens live in requests as `__DEVM_SECRET_<name>__`.
// iron-proxy's secrets transform does a literal find-and-replace per
// bound secret; the gate below mirrors that: it tests, per bound secret,
// whether the token appears in the request path.
const (
	secretTokenPrefix = "__DEVM_SECRET_"
	secretTokenSuffix = "__"
)

// secretPathRejectBody is the body returned on a path-unsafe reject.
// The format uses one placeholder (%q), the secret name. The leading
// text and the "misroute the request to a different endpoint" clause
// are frozen — tests pin substring matches on them.
const secretPathRejectBody = `devm proxy refused to substitute secret %q into URL path:
characters in the stored value would misroute the request to a
different endpoint. Pass the secret in an Authorization header or a
query parameter instead.
`

// secretPathUnsafeResponse returns the devm-authored 400 for a request
// whose path contains a bound-secret placeholder whose value contains
// U+002F ('/'). Only the secret NAME ends up in the response — never
// the value.
func secretPathUnsafeResponse(name string) *transformv1.HttpResponse {
	body := fmt.Sprintf(secretPathRejectBody, name)
	return &transformv1.HttpResponse{
		StatusCode: http.StatusBadRequest,
		Headers: map[string]*transformv1.HeaderValues{
			"X-Devm-Secret-Reject": {Values: []string{"path-unsafe"}},
			"X-Devm-Secret-Name":   {Values: []string{name}},
			"Content-Type":         {Values: []string{"text/plain; charset=utf-8"}},
		},
		Body: []byte(body),
	}
}

// policyService implements transformv1.TransformServiceServer for one
// project.
type policyService struct {
	transformv1.UnimplementedTransformServiceServer
	authority *PolicyAuthority
	projectID string
}

// secretGateReject scans path for __DEVM_SECRET_<name>__ tokens whose
// bound values contain '/', the only char iron-proxy's path writer
// does not escape. Returns the earliest-in-path offending secret name
// and true, or ("", false) if nothing in path is unsafe.
//
// Fast path: when path has no `__DEVM_SECRET_` substring at all, no
// backend reads happen. For requests that do carry a placeholder, one
// backend.List is issued, then one backend.Get per name whose token
// appears in path — the typical N is a handful of bound secrets.
//
// A nil backend (test contexts that did not wire one) returns
// ("", false) — gate inert. A backend List or Get error other than
// ErrNotFound is logged via daemonlog.Errorf and treated as fall-through:
// a transient read failure must not become a 400 the user has to debug.
func (s *policyService) secretGateReject(path string) (name string, rejected bool) {
	if path == "" || !strings.Contains(path, secretTokenPrefix) {
		return "", false
	}
	be := s.authority.secretBackend()
	if be == nil {
		return "", false
	}
	names, err := be.List(s.projectID)
	if err != nil {
		daemonlog.Errorf("policy: secret backend List(%s): %v", s.projectID, err)
		return "", false
	}
	bestName := ""
	bestPos := -1
	for _, n := range names {
		token := secretTokenPrefix + n + secretTokenSuffix
		pos := strings.Index(path, token)
		if pos < 0 {
			continue
		}
		v, err := be.Get(s.projectID + "/" + n)
		if err != nil {
			if errors.Is(err, secret.ErrNotFound) {
				// Listed but gone between List and Get — treat as unbound.
				continue
			}
			daemonlog.Errorf("policy: secret backend Get(%s/%s): %v", s.projectID, n, err)
			continue
		}
		if !strings.ContainsRune(v, '/') {
			continue
		}
		if bestPos < 0 || pos < bestPos {
			bestPos = pos
			bestName = n
		}
	}
	if bestPos < 0 {
		return "", false
	}
	return bestName, true
}

func (s *policyService) TransformRequest(ctx context.Context, req *transformv1.TransformRequestRequest) (*transformv1.TransformRequestResponse, error) {
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
	// Gate: a placeholder in the path whose bound value contains '/'
	// would silently misroute. Reject with a devm-authored 400 before the
	// allowlist check so the user sees the authoring bug even when the
	// destination host is allowed.
	if name, rejected := s.secretGateReject(reqPath); rejected {
		return &transformv1.TransformRequestResponse{
			Action:   transformv1.TransformAction_TRANSFORM_ACTION_REJECT,
			Response: secretPathUnsafeResponse(name),
		}, nil
	}
	if s.authority.decide(s.projectID, host, reqPath, r.GetMethod()) {
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
