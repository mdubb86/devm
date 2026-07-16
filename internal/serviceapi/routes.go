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
}

func NewRoutes() *Routes {
	return &Routes{
		projectsToHostnames: make(map[string][]string),
		hostnameToRoute:     make(map[string]Route),
	}
}

// Apply replaces the named project's routes with the given set.
func (r *Routes) Apply(projectID string, items []Route) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range r.projectsToHostnames[projectID] {
		delete(r.hostnameToRoute, h)
	}
	hostnames := make([]string, 0, len(items))
	for _, item := range items {
		r.hostnameToRoute[item.Hostname] = item
		hostnames = append(hostnames, item.Hostname)
	}
	r.projectsToHostnames[projectID] = hostnames
}

// Remove drops all routes for the project.
func (r *Routes) Remove(projectID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range r.projectsToHostnames[projectID] {
		delete(r.hostnameToRoute, h)
	}
	delete(r.projectsToHostnames, projectID)
}

// Lookup returns the route for the given host (port stripped).
func (r *Routes) Lookup(host string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	route, ok := r.hostnameToRoute[host]
	if ok && route.Direct {
		return Route{}, false // direct services are never proxy-dialed
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
		routes.Apply(req.Name, req.Routes)
		w.WriteHeader(http.StatusNoContent)
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
