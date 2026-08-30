package policymatch

import "testing"

func TestAllowed(t *testing.T) {
	tests := []struct {
		name  string
		allow []string
		host  string
		path  string
		want  bool
	}{
		// Host-only entries: exact.
		{"exact host", []string{"example.com"}, "example.com", "/x", true},
		{"exact host miss", []string{"example.com"}, "example.org", "/x", false},
		{"subdomain not covered by apex", []string{"example.com"}, "sub.example.com", "/", false},

		// Wildcard "*" matches any host.
		{"star matches all", []string{"*"}, "anything.at.all", "/", true},

		// "*.example.com" matches any subdomain depth AND the apex.
		{"wildcard subdomain", []string{"*.example.com"}, "a.example.com", "/", true},
		{"wildcard deep subdomain", []string{"*.example.com"}, "a.b.example.com", "/", true},
		{"wildcard matches apex", []string{"*.example.com"}, "example.com", "/", true},
		{"wildcard suffix miss", []string{"*.example.com"}, "notexample.com", "/", false},

		// Single-label glob via path.Match: "*" in pattern does not cross dots.
		{"glob single label", []string{"api-*.example.com"}, "api-v2.example.com", "/", true},
		{"glob does not cross dots", []string{"api-*.example.com"}, "api.v2.example.com", "/", false},

		// Path-scoped entries: only matching paths pass.
		{"path subtree", []string{"example.com/v1/*"}, "example.com", "/v1/models", true},
		{"path subtree base", []string{"example.com/v1/*"}, "example.com", "/v1", true},
		{"path subtree miss", []string{"example.com/v1/*"}, "example.com", "/v2/models", false},
		{"path exact glob", []string{"example.com/health"}, "example.com", "/health", true},
		{"path exact glob miss", []string{"example.com/health"}, "example.com", "/healthz", false},
		{"path entry other host", []string{"example.com/v1/*"}, "example.org", "/v1/x", false},

		// Path glob is segment-wise (path.Match): "*" doesn't cross "/".
		{"path glob segment", []string{"example.com/dl/*.tar.gz"}, "example.com", "/dl/x.tar.gz", true},
		{"path glob no slash cross", []string{"example.com/dl/*.tar.gz"}, "example.com", "/dl/a/x.tar.gz", false},

		// Host-only entry beats a path restriction on the same host.
		{"host-only widens path entry", []string{"example.com/v1/*", "example.com"}, "example.com", "/anything", true},

		// Multiple path entries OR together.
		{"multi path or", []string{"example.com/a/*", "example.com/b/*"}, "example.com", "/b/x", true},

		// Wildcard host with path pattern.
		{"wildcard host with path", []string{"*.github.com/repos/*"}, "api.github.com", "/repos/x", true},
		{"wildcard host with path miss", []string{"*.github.com/repos/*"}, "api.github.com", "/user", false},

		// Port on request host is the caller's job to strip; entries never carry ports.
		{"empty allowlist denies", nil, "example.com", "/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Allowed(tt.allow, tt.host, tt.path); got != tt.want {
				t.Errorf("Allowed(%v, %q, %q) = %v, want %v",
					tt.allow, tt.host, tt.path, got, tt.want)
			}
		})
	}
}

func TestStripPort(t *testing.T) {
	for in, want := range map[string]string{
		"example.com:443": "example.com",
		"example.com":     "example.com",
		"127.0.0.1:8080":  "127.0.0.1",
		"[::1]:443":       "::1",
	} {
		if got := StripPort(in); got != want {
			t.Errorf("StripPort(%q) = %q, want %q", in, got, want)
		}
	}
}
