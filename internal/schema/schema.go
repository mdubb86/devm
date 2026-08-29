package schema

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultDiskSizeGB is the virtual disk size (in GB) baked into the
// devm-base image and the floor for a per-project `disk:` override.
// The base image's root filesystem is grown to this during the base
// build; every project VM clones it. tart disks are sparse, so a
// larger ceiling costs nothing on the host until the guest writes to
// it. tart disk resize is grow-only, so overrides below this are
// rejected.
const DefaultDiskSizeGB = 32

// SecretRef is the in-memory representation of a YAML `!secret <name>`
// tagged value. Resolved to a literal at iron-proxy spawn time by
// reading <name> from the on-disk secret store
// (identity.Config.SecretsDir()).
type SecretRef struct {
	Name string
}

// EnvValue is either a literal string or a SecretRef. devm.yaml's
// env: map decodes to map[string]EnvValue.
type EnvValue struct {
	Literal string     // populated when Secret == nil
	Secret  *SecretRef // populated when the YAML value used !secret tag
}

// UnmarshalYAML decodes either a plain scalar or a !secret-tagged
// scalar into an EnvValue.
func (e *EnvValue) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!secret" {
		e.Secret = &SecretRef{Name: node.Value}
		return nil
	}
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("env value: expected scalar or !secret, got %v", node.Kind)
	}
	e.Literal = node.Value
	return nil
}

// MarshalYAML encodes an EnvValue as the same on-wire format that
// UnmarshalYAML reads: a plain scalar for literals, a !secret-tagged
// scalar for secrets. This makes yaml.Marshal(cfg) produce YAML that
// round-trips through yaml.Unmarshal(&cfg) without error — required
// for snapshot storage.
func (e EnvValue) MarshalYAML() (interface{}, error) {
	if e.Secret != nil {
		return &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!secret",
			Value: e.Secret.Name,
		}, nil
	}
	return e.Literal, nil
}

// IsSecret reports whether this env value is a secret reference.
func (e EnvValue) IsSecret() bool { return e.Secret != nil }

// TokenFor returns the deterministic opaque token devm uses to mark
// a secret in workload env. Same secret name → same token across
// process lifetimes so iron-proxy restarts don't strand stale tokens
// in the VM's env.
func TokenFor(secretName string) string {
	return fmt.Sprintf("__DEVM_SECRET_%s__", secretName)
}

// Render returns the value to emit into a systemd Environment= line
// or any other env-rendering context: the literal string, or the
// opaque token form for a SecretRef.
func (e EnvValue) Render() string {
	if e.Secret != nil {
		return TokenFor(e.Secret.Name)
	}
	return e.Literal
}

// volumeNameRE matches valid volume names. Enforced at load time
// because names become the mount tag suffix (`vol_<name>`) and a
// filesystem path segment; both need to be safe.
var volumeNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// validateVolumes checks the top-level Volumes map. Emitted errors
// name the offending volume key so users see exactly which entry to
// fix.
func (c Config) validateVolumes(workspaceRoot string) error {
	if len(c.Volumes) == 0 {
		return nil
	}
	// Sort for deterministic error messages under Go's random map order.
	names := make([]string, 0, len(c.Volumes))
	for n := range c.Volumes {
		names = append(names, n)
	}
	sort.Strings(names)

	// First: name/path shape checks per entry.
	for _, name := range names {
		if name == "" {
			return fmt.Errorf("volumes: name must not be empty")
		}
		if !volumeNameRE.MatchString(name) {
			return fmt.Errorf(`volumes: name %q must match [a-z0-9][a-z0-9._-]*`, name)
		}
		path := c.Volumes[name].Path
		if path == "" {
			return fmt.Errorf("volumes.%s: guest path must not be empty", name)
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf(`volumes.%s: guest path %q must be absolute`, name, path)
		}
		if strings.Contains(path, "..") {
			return fmt.Errorf(`volumes.%s: guest path %q must not contain ..`, name, path)
		}
	}

	// Second: cross-entry uniqueness of guest paths. Two volumes on
	// the same guest target would collide at mount time.
	byPath := map[string]string{}
	for _, name := range names {
		path := c.Volumes[name].Path
		if prior, ok := byPath[path]; ok {
			// Report the pair in name-sorted order — deterministic
			// message regardless of which name Go's iterator saw first.
			return fmt.Errorf(`volumes.%s: guest path %q already declared by volume %q`, name, path, prior)
		}
		byPath[path] = name
	}

	// Third: no overlap with the workspace mount root. The workspace
	// is virtiofs-mounted at the same absolute path in the guest as
	// on the Mac (mirrored per vm.go). A volume mounted at the
	// workspace root or any subpath would collide with the workspace
	// bind. Only enforceable when workspaceRoot is known
	// (ValidateWithRoot); plain Validate() with empty workspaceRoot
	// skips this check.
	if workspaceRoot == "" {
		return nil
	}
	cleanedRoot := filepath.Clean(workspaceRoot)
	rootPrefix := cleanedRoot + string(filepath.Separator)
	for _, name := range names {
		vp := filepath.Clean(c.Volumes[name].Path)
		if vp == cleanedRoot || strings.HasPrefix(vp, rootPrefix) {
			return fmt.Errorf(`volumes.%s: guest path %q overlaps the workspace mount root %q`,
				name, c.Volumes[name].Path, cleanedRoot)
		}
	}
	return nil
}

type Template struct {
	Source string `yaml:"source"`
	Output string `yaml:"output"`
	// Sudo escalates the installer to root when writing DEST. Default
	// false: the installer runs as the guest user (devm) and writes the
	// file devm-owned. Set true for /etc, /usr, /var — anywhere the guest
	// user can't write. Without sudo:true a failed write is a loud
	// cold-start error rather than a silent sudo fallback.
	Sudo bool `yaml:"sudo,omitempty"`
}

func (t Template) Validate() error {
	if t.Source == "" {
		return fmt.Errorf("template.source is required")
	}
	if t.Output == "" {
		return fmt.Errorf("template.output is required")
	}
	// Source must stay inside the project root. After filepath.Clean,
	// any traversal manifests as a leading "..". An absolute source
	// path also escapes the project root.
	cleaned := filepath.Clean(t.Source)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return fmt.Errorf("template.source %q: path traversal or absolute path not allowed", t.Source)
	}
	// Output must be absolute (lands inside the sandbox).
	if !filepath.IsAbs(t.Output) {
		return fmt.Errorf("template.output %q must be an absolute path", t.Output)
	}
	return nil
}

type Service struct {
	// Port is the sandbox-side listen port. Set via `port: 80` in
	// devm.yaml. Polymorphic with BindIP via custom YAML
	// (un)marshaling: writing `port: "0.0.0.0:80"` populates both
	// Port=80 AND BindIP="0.0.0.0" from a single field.
	Port int `yaml:"-"`

	// BindIP is the host-side interface for this service's port
	// mapping. Populated from the IP component of `port: "IP:PORT"`
	// in devm.yaml. When empty, the mapping binds to 127.0.0.1
	// (default; localhost-only). Setting "0.0.0.0" exposes the port
	// on all host interfaces — useful when other devices on the LAN
	// need to reach the service.
	BindIP string `yaml:"-"`

	Hostname string `yaml:"hostname,omitempty"`
	// Direct routes this service directly to the VM's IP instead of
	// through the daemon's guest-origin listener. For raw-TCP / non-HTTP
	// services (e.g. Postgres). Requires a hostname.
	Direct bool `yaml:"direct,omitempty"`

	// ExposeHost, when true, opts the service's hostname into devm's
	// shared LAN dispatcher (0.0.0.0:42000). Only valid when Hostname
	// is set — Service.Validate rejects otherwise. Independent of
	// Direct (direct:true on LAN is a future release).
	ExposeHost bool `yaml:"expose_host,omitempty"`

	Env       map[string]EnvValue `yaml:"env,omitempty"`
	Templates []Template          `yaml:"templates,omitempty"`

	// Tart-era service execution fields. Systemd is mutually exclusive
	// with the declarative fields (Exec, Restart, After, WorkDir, User).
	Exec    []string `yaml:"exec,omitempty"`
	WorkDir string   `yaml:"workdir,omitempty"`
	Restart string   `yaml:"restart,omitempty"`
	After   []string `yaml:"after,omitempty"`
	User    string   `yaml:"user,omitempty"`
	Systemd string   `yaml:"systemd,omitempty"`
}

// serviceYAML is the on-the-wire shape. `port` is a yaml.Node so we
// can decode it as either int or string and populate both Service.Port
// and Service.BindIP from a single field.
type serviceYAML struct {
	Port       yaml.Node           `yaml:"port,omitempty"`
	Hostname   string              `yaml:"hostname,omitempty"`
	Direct     bool                `yaml:"direct,omitempty"`
	ExposeHost bool                `yaml:"expose_host,omitempty"`
	Env        map[string]EnvValue `yaml:"env,omitempty"`
	Templates  []Template          `yaml:"templates,omitempty"`
	Exec       []string            `yaml:"exec,omitempty"`
	WorkDir    string              `yaml:"workdir,omitempty"`
	Restart    string              `yaml:"restart,omitempty"`
	After      []string            `yaml:"after,omitempty"`
	User       string              `yaml:"user,omitempty"`
	Systemd    string              `yaml:"systemd,omitempty"`
}

// serviceKnownFields lists the yaml keys serviceYAML accepts. Kept in
// sync with the tags on serviceYAML above; enforced by
// TestService_KnownFieldsMatchStruct so it never drifts.
var serviceKnownFields = []string{
	"port", "hostname", "direct", "expose_host", "env", "templates",
	"exec", "workdir", "restart", "after", "user", "systemd",
}

// UnmarshalYAML implements polymorphic decoding for the `port` field:
//   - int form: `port: 80` → Port=80, BindIP=""
//   - string form: `port: "0.0.0.0:80"` → Port=80, BindIP="0.0.0.0"
//
// Also rejects unknown keys. yaml.v3's KnownFields(true) at the decoder
// level does NOT propagate through custom UnmarshalYAML using
// node.Decode, so services would silently accept typos like
// `services.api.replicaz: 3` without this explicit check.
func (s *Service) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		known := make(map[string]bool, len(serviceKnownFields))
		for _, k := range serviceKnownFields {
			known[k] = true
		}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i].Value
			if !known[key] {
				return fmt.Errorf(
					"unknown field %q at service (line %d) — valid: %s",
					key, node.Content[i].Line,
					strings.Join(serviceKnownFields, ", "))
			}
		}
	}
	var raw serviceYAML
	if err := node.Decode(&raw); err != nil {
		return err
	}
	s.Hostname = raw.Hostname
	s.Direct = raw.Direct
	s.ExposeHost = raw.ExposeHost
	s.Env = raw.Env
	s.Templates = raw.Templates
	s.Exec = raw.Exec
	s.WorkDir = raw.WorkDir
	s.Restart = raw.Restart
	s.After = raw.After
	s.User = raw.User
	s.Systemd = raw.Systemd
	return s.decodePortNode(raw.Port)
}

func (s *Service) decodePortNode(n yaml.Node) error {
	if n.Kind == 0 {
		return nil // no port set
	}
	// Try int decode first (the common case).
	var asInt int
	if err := n.Decode(&asInt); err == nil {
		s.Port = asInt
		return nil
	}
	// Fall back to string "IP:PORT".
	var asStr string
	if err := n.Decode(&asStr); err != nil {
		return fmt.Errorf("port: must be an integer or an \"IP:PORT\" string")
	}
	ip, portStr, ok := strings.Cut(asStr, ":")
	if !ok {
		return fmt.Errorf("port %q: string form must be \"IP:PORT\"", asStr)
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("port %q: %q is not a valid IP address (note: IPv6 not currently supported — use IPv4)", asStr, ip)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("port %q: %q is not an integer", asStr, portStr)
	}
	s.Port = port
	s.BindIP = ip
	return nil
}

// MarshalYAML round-trips Service back to its polymorphic on-the-wire
// shape: emits `port: N` (int) when BindIP is empty, `port: "IP:N"`
// (string) when BindIP is set. Snapshots must round-trip so the diff
// machinery sees the same shape the user wrote.
func (s Service) MarshalYAML() (interface{}, error) {
	out := struct {
		Port       interface{}         `yaml:"port,omitempty"`
		Hostname   string              `yaml:"hostname,omitempty"`
		Direct     bool                `yaml:"direct,omitempty"`
		ExposeHost bool                `yaml:"expose_host,omitempty"`
		Env        map[string]EnvValue `yaml:"env,omitempty"`
		Templates  []Template          `yaml:"templates,omitempty"`
		Exec       []string            `yaml:"exec,omitempty"`
		WorkDir    string              `yaml:"workdir,omitempty"`
		Restart    string              `yaml:"restart,omitempty"`
		After      []string            `yaml:"after,omitempty"`
		User       string              `yaml:"user,omitempty"`
		Systemd    string              `yaml:"systemd,omitempty"`
	}{
		Hostname:   s.Hostname,
		Direct:     s.Direct,
		ExposeHost: s.ExposeHost,
		Env:        s.Env,
		Templates:  s.Templates,
		Exec:       s.Exec,
		WorkDir:    s.WorkDir,
		Restart:    s.Restart,
		After:      s.After,
		User:       s.User,
		Systemd:    s.Systemd,
	}
	if s.Port != 0 {
		if s.BindIP == "" {
			out.Port = s.Port
		} else {
			out.Port = fmt.Sprintf("%s:%d", s.BindIP, s.Port)
		}
	}
	return out, nil
}

// ResolveBind returns the host bind IP for this service's port mapping.
// Returns "127.0.0.1" when no bind was specified (default).
func (s Service) ResolveBind() string {
	if s.BindIP == "" {
		return "127.0.0.1"
	}
	return s.BindIP
}

func (s Service) Validate() error {
	if s.ExposeHost && s.Hostname == "" {
		return fmt.Errorf("expose_host: true requires hostname on service")
	}
	if s.Hostname != "" && !strings.HasSuffix(s.Hostname, ".test") {
		return fmt.Errorf("service.hostname: must end in .test (got %q)", s.Hostname)
	}
	if s.Direct && s.Hostname == "" {
		return fmt.Errorf("direct: true requires a hostname")
	}
	if s.BindIP != "" && s.Port == 0 {
		return fmt.Errorf("port bind interface requires a sandbox port")
	}
	if s.Port == 0 && len(s.Exec) == 0 && s.Systemd == "" {
		return fmt.Errorf("service must define a port, exec, or systemd")
	}

	// systemd override is mutually exclusive with declarative fields.
	if s.Systemd != "" {
		if len(s.Exec) > 0 || s.Restart != "" || len(s.After) > 0 ||
			s.WorkDir != "" || s.User != "" {
			return fmt.Errorf("service.systemd is mutually exclusive with exec/restart/after/workdir/user")
		}
	}

	// restart enum.
	switch s.Restart {
	case "", "no", "on-failure", "always":
		// ok
	default:
		return fmt.Errorf("service.restart: must be one of: no, on-failure, always (got %q)", s.Restart)
	}

	for i, t := range s.Templates {
		if err := t.Validate(); err != nil {
			return fmt.Errorf("templates[%d]: %w", i, err)
		}
	}
	return nil
}

type Project struct {
	Name string `yaml:"name"`
}

func (p Project) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("project.name is required")
	}
	// name is used as both the devm-owned identity namespace (a path
	// component under the runtime dir) and the literal Tart VM handle, so
	// it must be free of whitespace and path-escape characters.
	if strings.ContainsAny(p.Name, " \t\n\r") {
		return fmt.Errorf("project.name %q: whitespace not allowed", p.Name)
	}
	if strings.ContainsAny(p.Name, "/\\") || strings.Contains(p.Name, "..") {
		return fmt.Errorf("project.name %q: '/', '\\', and '..' not allowed", p.Name)
	}
	return nil
}

// CheckUnknownKeys scans raw devm.yaml bytes for keys that aren't
// part of the schema and returns an error listing them. Catches the
// silent-failure class where a user mistypes a key or pastes an
// example from an old version, and is the only signal a user gets for a
// key that was removed in a newer devm — there is no per-key migration
// pointer.
//
// Checks top-level keys + project-block + network-block keys. Per-service
// shape has more legitimate variation (kit-passthrough fields could grow)
// so it's not validated here.
//
// Must be kept in sync with Config's yaml tags by hand — Config has no
// custom UnmarshalYAML (adding one would swallow strictDecode's
// KnownFields(true) for every nested block; see
// internal/config/load.go).
var topLevelKnownFields = []string{
	"project", "base_image", "docker", "network", "env",
	"services", "install", "startup", "scripts", "path", "packages", "disk", "memory", "cpu",
	"volumes", "repos",
}

func CheckUnknownKeys(data []byte) error {
	knownProject := []string{
		"name", "proxy",
	}
	knownNetwork := []string{
		"allow",
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil // typed unmarshal will surface the parse error
	}
	if err := rejectUnknown(raw, topLevelKnownFields, "top-level"); err != nil {
		return err
	}
	if proj, ok := raw["project"].(map[string]any); ok {
		if err := rejectUnknown(proj, knownProject, "project"); err != nil {
			return err
		}
	}
	if net, ok := raw["network"].(map[string]any); ok {
		if err := rejectUnknown(net, knownNetwork, "network"); err != nil {
			return err
		}
	}
	// base_image: is retained as a top-level key for YAML compatibility
	// but must NOT have any children — the Tart image pipeline replaces
	// per-project image config. Any child here is a typo.
	if bi, ok := raw["base_image"].(map[string]any); ok {
		if err := rejectUnknown(bi, nil, "base_image"); err != nil {
			return err
		}
	}
	return nil
}

func rejectUnknown(m map[string]any, known []string, scope string) error {
	knownSet := make(map[string]bool, len(known))
	for _, k := range known {
		knownSet[k] = true
	}
	for k := range m {
		if !knownSet[k] {
			if len(known) == 0 {
				return fmt.Errorf(
					"unknown field %q at %s — this block accepts no fields",
					k, scope)
			}
			return fmt.Errorf("unknown field %q at %s — valid: %s",
				k, scope, strings.Join(known, ", "))
		}
	}
	return nil
}

// BaseImage is kept for YAML compatibility (the base_image: key is still
// recognized so old configs don't get an "unknown field" error before the
// user can migrate). It has no fields — Tart images are configured via
// the image pipeline, not per-project YAML flags.
type BaseImage struct{}

// AllowEntry is one entry in network.allow. It is written in YAML as
// either a bare host string (reachable, no secret injection) or a mapping
// {host, secrets} (reachable, and the named secrets may be substituted for
// that host). The secret name joins to a `!secret` env value elsewhere.
//
// Host may carry a path pattern after the hostname —
// "release-assets.githubusercontent.com/github-production-release-asset/834082440/*"
// — which scopes reachability to matching request paths (iron-proxy
// allowlist `rules`; a pattern ending "/*" matches the subtree, anything
// else is an exact segment-wise glob). Without a path the whole host is
// reachable. Secret injection scope is always the host part alone: the
// path gates reachability, never widens or narrows where secrets go.
type AllowEntry struct {
	Host    string
	Secrets []string
}

// HostPart returns the hostname portion of Host, without any path pattern.
func (a AllowEntry) HostPart() string {
	if i := strings.IndexByte(a.Host, '/'); i >= 0 {
		return a.Host[:i]
	}
	return a.Host
}

// PathPattern returns the path pattern portion of Host ("/..." inclusive
// of the leading slash), or "" for a whole-host entry.
func (a AllowEntry) PathPattern() string {
	if i := strings.IndexByte(a.Host, '/'); i >= 0 {
		return a.Host[i:]
	}
	return ""
}

// UnmarshalYAML accepts a scalar host or a {host, secrets} mapping.
func (a *AllowEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		a.Host = node.Value
		return nil
	}
	if node.Kind == yaml.MappingNode {
		var raw struct {
			Host    string   `yaml:"host"`
			Secrets []string `yaml:"secrets"`
		}
		if err := node.Decode(&raw); err != nil {
			return err
		}
		if raw.Host == "" {
			return fmt.Errorf("network.allow entry: host is required")
		}
		a.Host = raw.Host
		a.Secrets = raw.Secrets
		return nil
	}
	return fmt.Errorf("network.allow entry: expected host string or {host, secrets} mapping")
}

type Network struct {
	Allow []AllowEntry `yaml:"allow,omitempty"`
}

// validate checks each allow entry's host/path shape. The path pattern
// is matched by iron-proxy against req.URL.Path only, so query and
// fragment characters in a pattern would silently never match — reject
// them here instead of letting the entry ship dead.
func (n Network) validate() error {
	for i, e := range n.Allow {
		if e.Host == "" {
			return fmt.Errorf("network.allow[%d]: host is required", i)
		}
		if strings.Contains(e.Host, "://") {
			return fmt.Errorf("network.allow[%d]: %q: scheme prefix not allowed — write the bare host", i, e.Host)
		}
		if e.HostPart() == "" {
			return fmt.Errorf("network.allow[%d]: %q: host part is empty", i, e.Host)
		}
		p := e.PathPattern()
		if p == "" {
			continue
		}
		if p == "/" {
			return fmt.Errorf("network.allow[%d]: %q: trailing slash with no path pattern — drop it for the whole host, or add a pattern like /dl/*", i, e.Host)
		}
		if strings.ContainsAny(p, "?") {
			return fmt.Errorf("network.allow[%d]: %q: query strings never participate in path matching — drop everything from '?'", i, e.Host)
		}
		if strings.ContainsAny(p, "#") {
			return fmt.Errorf("network.allow[%d]: %q: fragments never participate in path matching — drop everything from '#'", i, e.Host)
		}
	}
	return nil
}

// Domains is the reachability list: every allow entry's host, in order.
func (n Network) Domains() []string {
	out := make([]string, 0, len(n.Allow))
	for _, e := range n.Allow {
		out = append(out, e.Host)
	}
	return out
}

// SecretHosts maps each secret name to the sorted, de-duplicated set of
// hosts that named it across allow entries — the injection scope union.
func (n Network) SecretHosts() map[string][]string {
	sets := map[string]map[string]bool{}
	for _, e := range n.Allow {
		for _, s := range e.Secrets {
			if sets[s] == nil {
				sets[s] = map[string]bool{}
			}
			// Host part only: iron-proxy secret rules are host-based,
			// and a path-scoped entry must not leak its path pattern
			// into the injection scope.
			sets[s][e.HostPart()] = true
		}
	}
	out := make(map[string][]string, len(sets))
	for s, hostSet := range sets {
		hosts := make([]string, 0, len(hostSet))
		for h := range hostSet {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		out[s] = hosts
	}
	return out
}

type Config struct {
	Project   Project   `yaml:"project"`
	BaseImage BaseImage `yaml:"base_image,omitempty"`

	// Docker turns on the first-class docker feature: devm installs
	// Docker Engine via the upstream get.docker.com script, registers
	// devm-runc-shim as the default OCI runtime so containers trust
	// iron-proxy's CA transparently, gates the bridge-CIDR host→container
	// reachability rule in the cfg-derived egress enforcement, and adds
	// Docker Hub hosts to the effective allowlist. Requires teardown to
	// toggle.
	Docker bool `yaml:"docker,omitempty"`

	Network  Network             `yaml:"network,omitempty"`
	Env      map[string]EnvValue `yaml:"env,omitempty"`
	Services map[string]Service  `yaml:"services,omitempty"`

	// Volumes are per-project named persistent stores. Each key is a
	// volume name; each value is the guest mount path. Data lives on
	// the Mac side under
	// ~/Library/Application Support/<daemon>/<projectID>/<label>/
	// and survives `devm teardown`. See docs/superpowers/specs/
	// 2026-08-01-persistent-volumes-design.md.
	Volumes map[string]Volume `yaml:"volumes,omitempty"`

	// Repos declares the project's git repos to hydrate at cold-start,
	// keyed by name. A nil/empty map means the project has no repo —
	// utility VMs that only run tools. Exactly one entry may set
	// Primary to mark the primary workspace repo.
	Repos map[string]RepoConfig `yaml:"repos,omitempty"`

	// Packages is a list of apt package names installed automatically
	// via `apt-get install -y` during Tart VM provisioning.
	Packages []string `yaml:"packages,omitempty"`

	// Install is the list of shell commands run ONCE at sandbox create
	// time, in declaration order, as root. Each command is executed
	// under `bash -e -o pipefail -c`, wrapped by with-devm-env.sh so
	// the project env (WORKSPACE_DIR, cfg.Env values, path: entries) is
	// live inside the command. A failing step aborts provisioning.
	//
	// Affordances from the base image (no apt-get update needed):
	//   * ncurses-term is preinstalled (modern terminfo for TUIs).
	//   * en_US.UTF-8 locale is generated so LANG/LC_* forwarding lands
	//     on a real locale.
	Install []string `yaml:"install,omitempty"`

	// Startup is the list of shell commands run on EVERY boot, in
	// declaration order, as root under `bash -o pipefail -c`, with open
	// network (before egress enforcement). Contrast with Install (once,
	// first boot) and services (every boot, enforced egress).
	Startup []string `yaml:"startup,omitempty"`

	// Scripts is the project's library of named multi-command shell
	// snippets. A script's key must match [a-z][a-z0-9-]* (kebab-case,
	// starts with a letter). Its value is an ordered list of shell
	// commands. When referenced from install: or startup: as a string
	// beginning with `>NAME`, the engine joins the commands with " && "
	// and runs them under one `bash -eo pipefail -c` (install:) or
	// emits them inline into startup.sh (startup: shares a shell
	// already). Variables set in step N are visible in step N+1.
	//
	// V1 scope: refs only from install: and startup:. No parameters,
	// no script-to-script calls.
	Scripts map[string][]string `yaml:"scripts,omitempty"`

	// Path is a list of directories prepended to PATH inside the
	// sandbox. Reaches all four executable entrypoints (install,
	// startup foreground, startup background, interactive shell) via
	// the same /etc/environment fan-out as cfg.Env.
	//
	// Final PATH shape inside the sandbox:
	//   <Path[0]>:<Path[1]>:...:/opt/devm/scripts:<system PATH>
	//
	// User entries win precedence over devm-internal scripts AND over
	// container defaults. Substitution: $WORKSPACE (or ${WORKSPACE})
	// expands to repoRoot at config load time. $$ → literal $. Any
	// other $VAR reference is an error. Entries must be absolute
	// (start with / or $WORKSPACE); empty entries and `~` expansion
	// are rejected.
	//
	// Bucket: LIVE — same as cfg.Env. New shells pick up the new
	// PATH on next `devm shell`; running shells don't.
	Path []string `yaml:"path,omitempty"`

	// Disk optionally overrides the VM's virtual disk size, e.g. "64G".
	// Units are gigabytes with a G/GB suffix; the magnitude must be a
	// positive integer of at least DefaultDiskSizeGB. nil = base image
	// default (DefaultDiskSizeGB). The disk is grown from the base
	// clone at create time and tart resize is grow-only, so changing
	// this field recreates the VM (teardown bucket).
	Disk *string `yaml:"disk,omitempty"`

	// Memory optionally overrides the VM's RAM, e.g. "8G", "16G".
	// nil = use image default (baked into devm-base). Applied via
	// `tart set --memory` at VM start; a change reconciles as
	// BucketRestartVM.
	Memory *string `yaml:"memory,omitempty"`

	// Cpu optionally overrides the VM's virtual CPU count.
	// nil = use image default. Applied via `tart set --cpu` at VM
	// start; a change reconciles as BucketRestartVM.
	Cpu *int `yaml:"cpu,omitempty"`
}

// ParseDiskSize parses a `disk:` value like "64G" or "64GB" into an
// integer number of gigabytes. Exported so reconcile / vm-start can
// call it after Config.Validate has guaranteed parseability.
func ParseDiskSize(s string) (int, error) {
	raw := strings.TrimSpace(s)
	num := raw
	upper := strings.ToUpper(num)
	switch {
	case strings.HasSuffix(upper, "GB"):
		num = num[:len(num)-2]
	case strings.HasSuffix(upper, "G"):
		num = num[:len(num)-1]
	default:
		return 0, fmt.Errorf("disk: %q must use a gigabyte suffix, e.g. \"64G\"", s)
	}
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("disk: %q must be a positive integer number of gigabytes, e.g. \"64G\"", s)
	}
	return n, nil
}

// ParseMemorySize parses a `memory:` value like "8G" or "64GB" into an
// integer number of megabytes. The suffix is required and
// case-insensitive; the magnitude must be a positive integer.
// Exported for use in internal/reconcile after Config.Validate has
// guaranteed parseability at load time.
func ParseMemorySize(s string) (int, error) {
	raw := strings.TrimSpace(s)
	num := raw
	upper := strings.ToUpper(num)
	switch {
	case strings.HasSuffix(upper, "GB"):
		num = num[:len(num)-2]
	case strings.HasSuffix(upper, "G"):
		num = num[:len(num)-1]
	default:
		return 0, fmt.Errorf("memory: %q must use a gigabyte suffix, e.g. \"8G\"", s)
	}
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("memory: %q must be a positive integer number of gigabytes, e.g. \"8G\"", s)
	}
	return n * 1024, nil
}

// reservedProjectIDs are devm-internal storage directory names under
// the daemon's Application Support root. A project.name colliding
// with one of these would shadow devm's own storage layout. Must
// agree with cmd/devm's purgeSkipDirs — same set of reserved names.
var reservedProjectIDs = map[string]bool{
	"bin": true, "state": true, "iron-proxy": true,
	"mutagen": true, "ssh": true, "secrets": true,
	"ca": true, "softnet-bin": true, "volumes": true,
}

// validateProjectIDReserved rejects a project.name that collides with
// a devm-internal storage directory name.
func (c *Config) validateProjectIDReserved() error {
	name := c.Project.Name
	if reservedProjectIDs[name] {
		return fmt.Errorf("project.name %q collides with a devm-internal storage dir — pick another name", name)
	}
	return nil
}

// GuestHomeDir is the guest-side parent for every repo clone. Every
// repo entity's guest path is `<GuestHomeDir>/<label>` and every
// $WORKSPACE substitution in cfg.Env resolves to the primary repo's
// guest path here (not the Mac cwd — under mutagen-volumes the guest
// no longer mirrors the Mac's absolute path).
const GuestHomeDir = "/home/devm"

// PrimaryRepoName returns the name of cfg.Repos' primary entry: the
// one explicitly marked Primary, or else the sole entry with a nil
// URL. Returns "" if no entry qualifies (no repos declared, or an
// ambiguous configuration that validateRepos would reject).
func (c *Config) PrimaryRepoName() string {
	var urlNilName string
	urlNilCount := 0
	for name, r := range c.Repos {
		if r.Primary != nil && *r.Primary {
			return name
		}
		if r.URL == nil {
			urlNilName = name
			urlNilCount++
		}
	}
	if urlNilCount == 1 {
		return urlNilName
	}
	return ""
}

// ResolveLabel returns the mutagen sync label for one repos.<name>
// entry: an explicit `label:` always wins; else a repo with a URL
// uses BareCloneName; else (the URL-nil primary) the basename of
// macCwd.
func (r RepoConfig) ResolveLabel(macCwd string) string {
	if r.Label != nil {
		return *r.Label
	}
	if r.URL != nil {
		return BareCloneName(*r.URL)
	}
	return filepath.Base(macCwd)
}

// PrimaryGuestPath returns cfg's primary repo's guest-side path
// (`/home/devm/<label>`), or "" if no repo is declared. This is the
// path that $WORKSPACE expands to in cfg.Env — under mutagen-volumes
// the guest workspace lives at a label-derived path, not the Mac
// project directory's absolute path.
func (c *Config) PrimaryGuestPath(macCwd string) string {
	name := c.PrimaryRepoName()
	if name == "" {
		return ""
	}
	return filepath.Join(GuestHomeDir, c.Repos[name].ResolveLabel(macCwd))
}

// BareCloneName derives a repo's default label from its clone URL:
// strips a trailing ".git", then keeps the path segment after the
// last "/" or ":" — "git@github.com:me/foo.git" → "foo".
func BareCloneName(url string) string {
	trimmed := strings.TrimSuffix(url, ".git")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	if i := strings.LastIndex(trimmed, ":"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	return trimmed
}

// cwdLabelPlaceholder is the label derived for a URL-nil primary repo
// when macCwd is unknown. Production callers always resolve through
// ValidateWithRoot, which supplies the real Mac cwd; this placeholder
// only surfaces when Validate() is called directly (unit tests).
const cwdLabelPlaceholder = "__cwd__"

// validateRepos enforces primary determination across Config.Repos:
// either exactly one entry is marked `primary: true`, or exactly one
// entry omits `url:` (implying primary — its URL derives from
// `git remote get-url origin` in the Mac cwd). The resolved primary
// must not have `volume: false`.
func (c *Config) validateRepos() error {
	if len(c.Repos) == 0 {
		return nil
	}
	names := make([]string, 0, len(c.Repos))
	for name := range c.Repos {
		names = append(names, name)
	}
	sort.Strings(names)

	var explicitPrimary []string
	var urlNilKeys []string
	for _, name := range names {
		r := c.Repos[name]
		if r.Primary != nil && *r.Primary {
			explicitPrimary = append(explicitPrimary, name)
		}
		if r.URL == nil {
			urlNilKeys = append(urlNilKeys, name)
		}
	}
	if len(explicitPrimary) > 1 {
		return fmt.Errorf("repos: multiple entries with primary: true (%v) — only one allowed", explicitPrimary)
	}

	var primaryName string
	switch {
	case len(explicitPrimary) == 1:
		primaryName = explicitPrimary[0]
	case len(urlNilKeys) == 1:
		primaryName = urlNilKeys[0]
	case len(urlNilKeys) == 0:
		return fmt.Errorf("repos: no primary — either mark one entry `primary: true` or omit `url:` on one entry (URL derives from `git remote get-url origin`)")
	default:
		return fmt.Errorf("repos: multiple entries without a `url:` (%v) — mark exactly one `primary: true`", urlNilKeys)
	}

	primary := c.Repos[primaryName]
	if primary.Volume != nil && !*primary.Volume {
		return fmt.Errorf("repos.%s: primary cannot have volume: false", primaryName)
	}

	// Secondary repos must declare URL explicitly (they don't derive from
	// `git remote get-url origin`; only the primary does).
	for _, name := range names {
		if name == primaryName {
			continue
		}
		if c.Repos[name].URL == nil {
			return fmt.Errorf("repos.%s: url is required for secondary repos (only the primary derives URL from `git remote get-url origin`)", name)
		}
	}
	return nil
}

// labelOwner names one entity (a repos: or volumes: entry) that
// derived or declared a given mutagen sync label.
type labelOwner struct {
	kind, name, label string
}

// validateLabels checks the flat label namespace shared by repos: and
// volumes: for collisions. An entry's label is its explicit `label:`
// when set, else derived: repos with url: → bare-clone name; the
// url-nil primary repo → basename of macCwd (or cwdLabelPlaceholder
// when macCwd is unknown); volumes → leaf-dir of path:.
func (c *Config) validateLabels(macCwd string) error {
	var owners []labelOwner

	repoNames := make([]string, 0, len(c.Repos))
	for name := range c.Repos {
		repoNames = append(repoNames, name)
	}
	sort.Strings(repoNames)
	for _, name := range repoNames {
		r := c.Repos[name]
		var label string
		switch {
		case r.Label != nil:
			label = *r.Label
		case r.URL != nil:
			label = BareCloneName(*r.URL)
		case macCwd != "":
			label = filepath.Base(macCwd)
		default:
			label = cwdLabelPlaceholder
		}
		owners = append(owners, labelOwner{"repos", name, label})
	}

	volNames := make([]string, 0, len(c.Volumes))
	for name := range c.Volumes {
		volNames = append(volNames, name)
	}
	sort.Strings(volNames)
	for _, name := range volNames {
		v := c.Volumes[name]
		var label string
		if v.Label != nil {
			label = *v.Label
		} else {
			label = filepath.Base(v.Path)
		}
		owners = append(owners, labelOwner{"volumes", name, label})
	}

	seen := map[string]labelOwner{}
	for _, o := range owners {
		if prior, ok := seen[o.label]; ok {
			return fmt.Errorf(
				"label %q: %s.%s and %s.%s both resolve to it — set an explicit `label:` on one",
				o.label, prior.kind, prior.name, o.kind, o.name)
		}
		seen[o.label] = o
	}
	return nil
}

// ValidateWithRoot is like Validate but additionally checks
// project-root-relative config (volumes, labels) against the
// filesystem. Callers that have the project root (devm's config
// loader) should prefer ValidateWithRoot; the parameter-free Validate
// skips those checks.
func (c Config) ValidateWithRoot(projectRoot string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.validateVolumes(projectRoot); err != nil {
		return err
	}
	if err := c.validateLabels(projectRoot); err != nil {
		return err
	}
	return nil
}

func (c Config) Validate() error {
	if err := c.Project.Validate(); err != nil {
		return err
	}
	if err := c.Network.validate(); err != nil {
		return err
	}
	if c.Disk != nil {
		gib, err := ParseDiskSize(*c.Disk)
		if err != nil {
			return err
		}
		if gib < DefaultDiskSizeGB {
			return fmt.Errorf("disk: %dG is below the %dG minimum (the base image default)", gib, DefaultDiskSizeGB)
		}
	}
	if c.Memory != nil {
		if _, err := ParseMemorySize(*c.Memory); err != nil {
			return err
		}
	}
	if c.Cpu != nil {
		if *c.Cpu <= 0 {
			return fmt.Errorf("cpu: %d must be a positive integer", *c.Cpu)
		}
	}
	for i, ic := range c.Install {
		if ic == "" {
			return fmt.Errorf("install[%d] must not be empty", i)
		}
	}
	for i, sc := range c.Startup {
		if sc == "" {
			return fmt.Errorf("startup[%d] must not be empty", i)
		}
	}
	// Scripts: validate each script's name and body before checking refs.
	{
		names := make([]string, 0, len(c.Scripts))
		for name := range c.Scripts {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := ValidateScriptName(name); err != nil {
				return fmt.Errorf("scripts: %w", err)
			}
			if len(c.Scripts[name]) == 0 {
				return fmt.Errorf("scripts[%s]: script body must not be empty", name)
			}
			for i, cmd := range c.Scripts[name] {
				if cmd == "" {
					return fmt.Errorf("scripts[%s][%d] must not be empty", name, i)
				}
				if _, ok := ParseScriptRef(cmd); ok {
					return fmt.Errorf("scripts[%s][%d]: script-to-script refs are not supported (V1)", name, i)
				}
			}
		}
		// Install: refs — check name resolves.
		for i, entry := range c.Install {
			if refName, ok := ParseScriptRef(entry); ok {
				if err := ValidateScriptName(refName); err != nil {
					return fmt.Errorf("install[%d]: %w", i, err)
				}
				if _, exists := c.Scripts[refName]; !exists {
					return fmt.Errorf("install[%d]: reference to undefined script %q", i, refName)
				}
			}
		}
		// Startup: refs — same check.
		for i, entry := range c.Startup {
			if refName, ok := ParseScriptRef(entry); ok {
				if err := ValidateScriptName(refName); err != nil {
					return fmt.Errorf("startup[%d]: %w", i, err)
				}
				if _, exists := c.Scripts[refName]; !exists {
					return fmt.Errorf("startup[%d]: reference to undefined script %q", i, refName)
				}
			}
		}
	}
	names := make([]string, 0, len(c.Services))
	for name := range c.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	seenHosts := make(map[string]string)
	seenPorts := make(map[int]string)
	for _, name := range names {
		svc := c.Services[name]
		if err := svc.Validate(); err != nil {
			return fmt.Errorf("services.%s: %w", name, err)
		}
		if svc.Hostname != "" {
			if prev, ok := seenHosts[svc.Hostname]; ok {
				return fmt.Errorf("duplicate hostname %q in services %s and %s", svc.Hostname, prev, name)
			}
			seenHosts[svc.Hostname] = name
		}
		if svc.Port != 0 {
			if svc.Port < 1 || svc.Port > 65535 {
				return fmt.Errorf("services.%s: port %d out of range (1-65535)", name, svc.Port)
			}
			if prev, ok := seenPorts[svc.Port]; ok {
				return fmt.Errorf("duplicate port %d in services %s and %s", svc.Port, prev, name)
			}
			seenPorts[svc.Port] = name
		}
	}
	if err := c.validateVolumes(""); err != nil {
		return err
	}
	if err := c.validateSecretBindings(); err != nil {
		return err
	}
	if err := c.validateRepos(); err != nil {
		return err
	}
	if err := c.validateProjectIDReserved(); err != nil {
		return err
	}
	if err := c.validateLabels(""); err != nil {
		return err
	}
	return nil
}

// validateSecretBindings checks that every secret is named on both sides
// of the config: `env:` (which delivers the opaque token into the guest)
// and `network.allow[].secrets` (which scopes the hosts iron-proxy may
// substitute it for). Either half alone is inert, and inert in a way
// that surfaces as an upstream 401 rather than as a devm error —
// requests carry the literal `__DEVM_SECRET_<name>__` token.
//
// The two halves aren't redundant: `env:` binds a secret to one or more
// environment variable names (GH_TOKEN and GITHUB_TOKEN can both carry
// one github_token), while `network.allow` binds it to hosts. Only the
// name is shared, so only the name can be cross-checked.
func (c Config) validateSecretBindings() error {
	envRefs := map[string]bool{}
	collect := func(env map[string]EnvValue) {
		for _, v := range env {
			if v.IsSecret() {
				envRefs[v.Secret.Name] = true
			}
		}
	}
	collect(c.Env)
	for _, svc := range c.Services {
		collect(svc.Env)
	}

	hosts := c.Network.SecretHosts()

	unscoped := make([]string, 0, len(envRefs))
	for name := range envRefs {
		if len(hosts[name]) == 0 {
			unscoped = append(unscoped, name)
		}
	}
	sort.Strings(unscoped)
	if len(unscoped) > 0 {
		return fmt.Errorf(
			"secret %s referenced in env but bound to no host — add it to a network.allow entry's secrets: list, or requests will carry the unsubstituted token",
			strings.Join(quoteAll(unscoped), ", "))
	}

	undelivered := make([]string, 0, len(hosts))
	for name := range hosts {
		if !envRefs[name] {
			undelivered = append(undelivered, name)
		}
	}
	sort.Strings(undelivered)
	if len(undelivered) > 0 {
		return fmt.Errorf(
			"secret %s bound to a host in network.allow but never referenced by an env value — add `SOME_VAR: !secret <name>` under env:, or it is never delivered to the guest",
			strings.Join(quoteAll(undelivered), ", "))
	}
	return nil
}

func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strconv.Quote(n)
	}
	return out
}
