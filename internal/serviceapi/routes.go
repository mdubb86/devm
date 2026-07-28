package serviceapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// RouteMode is what the proxy dials to reach the backend.
type RouteMode int

const (
	ModeVM    RouteMode = iota // dial the VM's IP on the service's port
	ModeLocal                  // dial Mac canonical port
)

func (m RouteMode) String() string {
	switch m {
	case ModeVM:
		return "vm"
	case ModeLocal:
		return "local"
	}
	return "unknown"
}

// Route is one hostname → backend mapping.
type Route struct {
	Hostname    string    `json:"hostname"`
	BackendHost string    `json:"backend_host,omitempty"` // defaults to localhost when empty
	BackendPort int       `json:"backend_port"`
	Mode        RouteMode `json:"mode"`
	// Direct marks a service reached directly at the VM's IP (no proxy).
	// The HTTP proxy refuses to dial it; DNS answers VM_IP for it.
	Direct  bool   `json:"direct,omitempty"`
	Project string `json:"project,omitempty"` // owning project; used by DNS to find the VM IP
	// ExposeHost mirrors Service.ExposeHost — true when this route
	// participates in the shared LAN dispatcher (0.0.0.0:42000).
	ExposeHost bool `json:"expose_host,omitempty"`
}

// Routes is the daemon's thread-safe in-memory route table. The
// proxy reads on every request via Lookup; the admin API mutates
// via Apply/Remove.
type Routes struct {
	mu sync.RWMutex
	// projectsToHostnames lets us efficiently remove all routes for
	// a project on teardown.
	projectsToHostnames map[string][]string
	// hostnameToRoute is the lookup path the proxy hits per request.
	hostnameToRoute map[string]Route
	// lanHostnameToRoute is the parallel opt-in map read by the LAN
	// dispatcher. Populated in Apply for routes with ExposeHost=true.
	lanHostnameToRoute map[string]Route
}

func NewRoutes() *Routes {
	return &Routes{
		projectsToHostnames: make(map[string][]string),
		hostnameToRoute:     make(map[string]Route),
		lanHostnameToRoute:  make(map[string]Route),
	}
}

// Apply replaces the named project's routes with the given set. Returns
// an error — without mutating any state — if any incoming hostname is
// already owned by a different project.
func (r *Routes) Apply(projectID string, items []Route) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Collision check first — atomic. If any incoming hostname is
	// already owned by a different project, reject the whole batch
	// without mutating either map.
	for _, item := range items {
		if existing, ok := r.hostnameToRoute[item.Hostname]; ok {
			if existing.Project != projectID {
				return fmt.Errorf(
					"hostname %q already registered by project %q — cannot register under %q",
					item.Hostname, existing.Project, projectID,
				)
			}
		}
	}

	// Clear this project's prior hostnames from both maps.
	for _, h := range r.projectsToHostnames[projectID] {
		delete(r.hostnameToRoute, h)
		delete(r.lanHostnameToRoute, h)
	}

	hostnames := make([]string, 0, len(items))
	for _, item := range items {
		r.hostnameToRoute[item.Hostname] = item
		if item.ExposeHost {
			r.lanHostnameToRoute[item.Hostname] = item
		}
		hostnames = append(hostnames, item.Hostname)
	}
	r.projectsToHostnames[projectID] = hostnames
	return nil
}

// Remove drops all routes for the project.
func (r *Routes) Remove(projectID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range r.projectsToHostnames[projectID] {
		delete(r.hostnameToRoute, h)
		delete(r.lanHostnameToRoute, h)
	}
	delete(r.projectsToHostnames, projectID)
}

// LANLookup returns the route for host from the LAN opt-in map — no
// per-project scope filter (LAN dispatch is Host-header only).
func (r *Routes) LANLookup(host string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	route, ok := r.lanHostnameToRoute[host]
	return route, ok
}

// CountLANRoutes returns the number of routes currently opted into the
// LAN dispatcher. Used by the LAN-listener lifecycle reconciler to
// decide whether the listener should be bound.
func (r *Routes) CountLANRoutes() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.lanHostnameToRoute)
}

// Lookup returns the route for the given host (port stripped), scoped
// to project: a route whose Project doesn't match the caller's project
// is refused even though the hostname exists — this is the isolation
// guarantee that keeps one project's proxy dispatch from ever
// reaching another project's backend. Pass "" to skip the project
// check (used by callers that have already established project scope
// some other way).
func (r *Routes) Lookup(host, project string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	route, ok := r.hostnameToRoute[host]
	if !ok || route.Direct {
		return Route{}, false // direct services are never proxy-dialed
	}
	if project != "" && route.Project != project {
		return Route{}, false // isolation guarantee: cross-project sneak-through denied
	}
	return route, ok
}

// DirectRoute returns the direct route for host, if one exists. Used by
// the DNS server to decide whether to answer VM_IP.
func (r *Routes) DirectRoute(host string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	route, ok := r.hostnameToRoute[host]
	if !ok || !route.Direct {
		return Route{}, false
	}
	return route, true
}

// AllByProject is used by GET /routes to render the full table.
// Returns a copy so callers can't mutate internals.
func (r *Routes) AllByProject() map[string][]Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]Route, len(r.projectsToHostnames))
	for proj, hosts := range r.projectsToHostnames {
		entries := make([]Route, 0, len(hosts))
		for _, h := range hosts {
			if route, ok := r.hostnameToRoute[h]; ok {
				entries = append(entries, route)
			}
		}
		out[proj] = entries
	}
	return out
}

// stripPort strips ":1234" from "host:1234".
func stripPort(host string) string {
	if i := strings.LastIndex(host, ":"); i >= 0 {
		port := host[i+1:]
		allDigits := len(port) > 0
		for _, c := range port {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return host[:i]
		}
	}
	return host
}

// ---------- admin HTTP handlers ----------

// ApplyRequest is the body shape for POST /routes/apply.
type ApplyRequest struct {
	Name   string  `json:"name"`
	Routes []Route `json:"routes"`
}

// ApplyResponse is the 200 body from POST /routes/apply. Routes carries
// the routes as the daemon stored them — for vm-mode non-direct routes,
// BackendHost is now populated with the substituted projectIP so the
// CLI can print the real upstream, not "localhost".
type ApplyResponse struct {
	Routes []Route `json:"routes"`
}

// RemoveRequest is the body shape for POST /routes/remove.
type RemoveRequest struct {
	Name string `json:"name"`
}

// RoutingStatus is what `devm status` displays for the Routing
// section. Built by the orchestrator from /routes admin call.
type RoutingStatus struct {
	Proxy          string        `json:"proxy"`
	ProxyReachable bool          `json:"proxy_reachable"`
	Mode           string        `json:"mode"`
	Routes         []RouteStatus `json:"routes"`
}

// RouteStatus is one row of the routing section in `devm status`.
type RouteStatus struct {
	Hostname string `json:"hostname"`
	Dial     string `json:"dial"`
	Mode     string `json:"mode"` // "local" | "vm" | "unknown"
}

// RegisterRoutesHandlers adds the three /routes endpoints to the
// given server's mux. Called once from runner.go after the Routes
// instance is created.
func RegisterRoutesHandlers(s *Server, routes *Routes) {
	s.Register("/routes/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req ApplyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("bad json: %v", err), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		// Substitute BackendHost = projectIP for vm-mode non-direct routes.
		// Rule is driven by Mode + Direct — the CLI-side rule that leaves
		// BackendHost unset for these routes is a *consequence* of this
		// substitution rule, not a signal to it.
		resolved := make([]Route, 0, len(req.Routes))
		for _, rt := range req.Routes {
			if rt.Mode == ModeVM && !rt.Direct {
				info, ok := ironProxyState.get(req.Name)
				if !ok || info.ProjectIP == "" {
					http.Error(w,
						fmt.Sprintf("no projectIP allocated for %q — start the VM first: `devm start`", req.Name),
						http.StatusBadRequest)
					return
				}
				rt.BackendHost = info.ProjectIP
			}
			resolved = append(resolved, rt)
		}
		if err := routes.Apply(req.Name, resolved); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ApplyResponse{Routes: resolved})
	})

	s.Register("/routes/remove", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req RemoveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("bad json: %v", err), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		routes.Remove(req.Name)
		w.WriteHeader(http.StatusNoContent)
	})

	s.Register("/routes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(routes.AllByProject())
	})
}
