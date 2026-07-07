package schema

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// SecretRef is the in-memory representation of a YAML `!secret <name>`
// tagged value. Resolved to a literal at iron-proxy spawn time by
// looking up <name> in the macOS login keychain.
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

type Mask struct {
	Path string `yaml:"path"`
	Size string `yaml:"size"`
}

func (m Mask) Validate() error {
	if m.Path == "" {
		return fmt.Errorf("mask.path is required")
	}
	if m.Size == "" {
		return fmt.Errorf("mask.size is required")
	}
	// Mask paths overlay locations inside the workspace; the renderer
	// prepends repoRoot. Reject anything that would silently produce
	// a broken mount: absolute paths, unexpanded shell-style variables
	// ($VAR, ${VAR}) and ~ (no expansion happens here), and traversal
	// that escapes the repo root.
	if filepath.IsAbs(m.Path) || strings.HasPrefix(m.Path, "~") || strings.HasPrefix(m.Path, "$") {
		return fmt.Errorf("mask.path %q must be relative to the repo root (no leading /, ~, or $)", m.Path)
	}
	cleaned := filepath.Clean(m.Path)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("mask.path %q: path traversal outside the repo root is not allowed", m.Path)
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

	Hostname  string               `yaml:"hostname,omitempty"`
	Env       map[string]EnvValue  `yaml:"env,omitempty"`
	Masks     []Mask               `yaml:"masks,omitempty"`
	Templates []Template           `yaml:"templates,omitempty"`

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
	Port      yaml.Node            `yaml:"port,omitempty"`
	Hostname  string               `yaml:"hostname,omitempty"`
	Env       map[string]EnvValue  `yaml:"env,omitempty"`
	Masks     []Mask               `yaml:"masks,omitempty"`
	Templates []Template           `yaml:"templates,omitempty"`
	Exec      []string             `yaml:"exec,omitempty"`
	WorkDir   string               `yaml:"workdir,omitempty"`
	Restart   string               `yaml:"restart,omitempty"`
	After     []string             `yaml:"after,omitempty"`
	User      string               `yaml:"user,omitempty"`
	Systemd   string               `yaml:"systemd,omitempty"`
}

// UnmarshalYAML implements polymorphic decoding for the `port` field:
//   - int form: `port: 80` → Port=80, BindIP=""
//   - string form: `port: "0.0.0.0:80"` → Port=80, BindIP="0.0.0.0"
func (s *Service) UnmarshalYAML(node *yaml.Node) error {
	var raw serviceYAML
	if err := node.Decode(&raw); err != nil {
		return err
	}
	s.Hostname = raw.Hostname
	s.Env = raw.Env
	s.Masks = raw.Masks
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
		Port      interface{}          `yaml:"port,omitempty"`
		Hostname  string               `yaml:"hostname,omitempty"`
		Env       map[string]EnvValue  `yaml:"env,omitempty"`
		Masks     []Mask               `yaml:"masks,omitempty"`
		Templates []Template           `yaml:"templates,omitempty"`
		Exec      []string             `yaml:"exec,omitempty"`
		WorkDir   string               `yaml:"workdir,omitempty"`
		Restart   string               `yaml:"restart,omitempty"`
		After     []string             `yaml:"after,omitempty"`
		User      string               `yaml:"user,omitempty"`
		Systemd   string               `yaml:"systemd,omitempty"`
	}{
		Hostname:  s.Hostname,
		Env:       s.Env,
		Masks:     s.Masks,
		Templates: s.Templates,
		Exec:      s.Exec,
		WorkDir:   s.WorkDir,
		Restart:   s.Restart,
		After:     s.After,
		User:      s.User,
		Systemd:   s.Systemd,
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
	if s.Hostname != "" && !strings.HasSuffix(s.Hostname, ".test") {
		return fmt.Errorf("service.hostname: must end in .test (got %q)", s.Hostname)
	}
	if s.BindIP != "" && s.Port == 0 {
		return fmt.Errorf("port bind interface requires a sandbox port")
	}
	if s.Port == 0 && len(s.Masks) == 0 && len(s.Exec) == 0 && s.Systemd == "" {
		return fmt.Errorf("service must define a port, at least one mask, exec, or systemd")
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

	for i, m := range s.Masks {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("masks[%d]: %w", i, err)
		}
	}
	for i, t := range s.Templates {
		if err := t.Validate(); err != nil {
			return fmt.Errorf("templates[%d]: %w", i, err)
		}
	}
	return nil
}

type Project struct {
	ID     string `yaml:"id"`
	VMName string `yaml:"vm_name"`
	Proxy  string `yaml:"proxy,omitempty"` // "caddy" (default) or "none"
}

func (p Project) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("project.id is required")
	}
	if p.VMName == "" {
		return fmt.Errorf("project.vm_name is required")
	}
	switch p.Proxy {
	case "", "caddy", "none":
	default:
		return fmt.Errorf("project.proxy: must be empty, 'caddy', or 'none' (got %q)", p.Proxy)
	}
	return nil
}

// CheckUnknownKeys scans raw devm.yaml bytes for keys that aren't
// part of the schema and returns an error listing them. Catches the
// silent-failure class where a user mistypes a key or pastes an
// example from an old version. Run alongside CheckLegacyKeys before
// the typed unmarshal.
//
// Checks top-level keys + project-block keys. Per-service shape has
// more legitimate variation (kit-passthrough fields could grow) so
// it's not validated here.
func CheckUnknownKeys(data []byte) error {
	knownTop := []string{
		"project", "base_image", "network", "env",
		"services", "install", "mounts", "path", "packages",
	}
	knownProject := []string{
		"id", "vm_name", "proxy",
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil // typed unmarshal will surface the parse error
	}
	if err := rejectUnknown(raw, knownTop, "top-level"); err != nil {
		return err
	}
	if proj, ok := raw["project"].(map[string]any); ok {
		if err := rejectUnknown(proj, knownProject, "project"); err != nil {
			return err
		}
	}
	// base_image: is retained as a top-level key for YAML compatibility
	// but must NOT have any children — the Tart image pipeline replaces
	// per-project image config. Any child here is either legacy (caught
	// by CheckLegacyKeys with a migration message) or a typo.
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

// CheckLegacyKeys scans raw devm.yaml bytes for fields that were once
// supported but have since been removed, returning a migration-pointer
// error rather than letting yaml.Unmarshal silently drop the value.
//
// Run BEFORE the typed unmarshal so the user sees the migration message
// instead of a downstream validation error.
func CheckLegacyKeys(data []byte) error {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// Not a migration concern; let the typed parse surface the real
		// syntax error.
		return nil
	}
	proj, ok := raw["project"].(map[string]any)
	if !ok {
		return nil
	}
	if _, hasApex := proj["hostname_apex"]; hasApex {
		return fmt.Errorf(
			"project.hostname_apex is no longer supported. " +
				"Move the value into env: HOSTNAME_APEX and update " +
				"templates from {{.Project.HostnameApex}} to " +
				"{{.Env.HOSTNAME_APEX}}.")
	}
	if _, hasSN := proj["sandbox_name"]; hasSN {
		return fmt.Errorf(
			"project.sandbox_name is no longer supported. " +
				"Use project.vm_name instead (rename the key in devm.yaml).")
	}
	if net, ok := raw["network"].(map[string]any); ok {
		if _, hasAD := net["allowed_domains"]; hasAD {
			return fmt.Errorf(
				"network.allowed_domains is no longer supported. " +
					"Use network.allow instead (rename the key in devm.yaml).")
		}
	}
	if bi, ok := raw["base_image"].(map[string]any); ok {
		if _, hasDocker := bi["docker"]; hasDocker {
			return fmt.Errorf(
				"base_image.docker is no longer supported. " +
					"Devm builds a single Tart-based devm-base image via the " +
					"internal image pipeline; per-project docker fallback was " +
					"removed. Remove the base_image block from devm.yaml.")
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
type AllowEntry struct {
	Host    string
	Secrets []string
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
			sets[s][e.Host] = true
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
	Project   Project              `yaml:"project"`
	BaseImage BaseImage            `yaml:"base_image,omitempty"`
	Network   Network              `yaml:"network,omitempty"`
	Env       map[string]EnvValue  `yaml:"env,omitempty"`
	Services  map[string]Service   `yaml:"services,omitempty"`

	// Packages is a list of apt package names installed automatically
	// via `apt-get install -y` during Tart VM provisioning.
	Packages []string `yaml:"packages,omitempty"`

	// Install is the list of shell commands run ONCE at sandbox create
	// time, in declaration order, as root. Each entry is wrapped by
	// .devm/scripts/wrap-fg.sh so its stdout+stderr is captured to
	// /tmp/.devm-install/install-<N>/current and an exit-code marker
	// is written. The supervision design surfaces failures with the
	// captured output via the install gate in `devm shell`.
	//
	// Affordances provided by devm's bootstrap step (runs FIRST, before
	// any user install entry):
	//   * `apt-get update` has already run, so user install entries can
	//     `apt-get install -y <pkg>` directly without a preceding update.
	//   * The `ncurses-term` package is installed (modern terminfo for TUIs).
	//     Devm embeds a static `s6-log` at `.devm/scripts/s6-log` (used by
	//     wrap-bg.sh for rotated background daemon logs — no apt step needed).
	//
	// Reserved arg-separator: `--` in a user command's argv is consumed
	// by the wrapper. Quote it or split into multiple steps.
	Install []string `yaml:"install,omitempty"`

	// Mounts are additional host paths shared into the VM at the same
	// path inside the VM ("mirrored path" mode — same host and guest
	// path). Each entry is a string of the form `HOST_PATH[:ro]`.
	// HOST_PATH may be absolute, relative to the project root, or
	// start with `~` for home-directory expansion. The optional `:ro`
	// suffix is passed through to tart's `--dir` flag and makes the
	// virtio-fs share read-only.
	//
	// Changing this field is in the TEARDOWN bucket: tart run's
	// --dir mounts are baked at VM-start time and the VM must be
	// stopped and re-started to apply.
	Mounts []string `yaml:"mounts,omitempty"`

	// Path is a list of directories prepended to PATH inside the
	// sandbox. Reaches all four executable entrypoints (install,
	// startup foreground, startup background, interactive shell) via
	// the same .devm/.env fan-out as cfg.Env.
	//
	// Final PATH shape inside the sandbox:
	//   <Path[0]>:<Path[1]>:...:$WORKSPACE/.devm/scripts:$PATH
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
}

// ResolveMount expands and absolute-resolves a single mounts[] entry
// against the given project root. Returns the canonical form
// `ABS_HOST_PATH[:ro]` ready to pass to tart's `--dir` flag.
//
// Rules:
//   - Optional `:ro` suffix is preserved (becomes `:ro` on the
//     `--dir` argument, which tart honors as a read-only share).
//   - A leading `~/` is expanded to the host user's home directory.
//   - Relative paths are joined to projectRoot.
//   - `filepath.Clean` is applied so `..` segments are resolved.
//
// Returns an error if entry is empty or if `~` expansion fails.
// Does NOT check whether the resolved host path exists — that's a
// separate concern (Validate does the existence check).
func ResolveMount(entry, projectRoot string) (string, error) {
	if entry == "" {
		return "", fmt.Errorf("mount entry must not be empty")
	}
	path, ro := strings.CutSuffix(entry, ":ro")
	if path == "" {
		return "", fmt.Errorf("mount entry %q: host path is empty", entry)
	}
	switch {
	case path == "~":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("mount entry %q: expand ~: %w", entry, err)
		}
		path = home
	case strings.HasPrefix(path, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("mount entry %q: expand ~/: %w", entry, err)
		}
		path = filepath.Join(home, path[2:])
	case !filepath.IsAbs(path):
		path = filepath.Join(projectRoot, path)
	}
	path = filepath.Clean(path)
	if ro {
		path += ":ro"
	}
	return path, nil
}

// ValidateWithRoot is like Validate but additionally checks the
// `mounts:` entries resolve cleanly and the resolved host paths
// exist. Callers that have the project root (devm's config loader)
// should prefer ValidateWithRoot; the parameter-free Validate skips
// path-existence checks.
func (c Config) ValidateWithRoot(projectRoot string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	for i, entry := range c.Mounts {
		resolved, err := ResolveMount(entry, projectRoot)
		if err != nil {
			return fmt.Errorf("mounts[%d]: %w", i, err)
		}
		hostPath, _ := strings.CutSuffix(resolved, ":ro")
		if _, err := os.Stat(hostPath); err != nil {
			return fmt.Errorf("mounts[%d]: host path %q: %w", i, hostPath, err)
		}
	}
	return nil
}

func (c Config) Validate() error {
	if err := c.Project.Validate(); err != nil {
		return err
	}
	for i, ic := range c.Install {
		if ic == "" {
			return fmt.Errorf("install[%d] must not be empty", i)
		}
	}
	for i, entry := range c.Mounts {
		if entry == "" {
			return fmt.Errorf("mounts[%d] must not be empty", i)
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
	// Mask paths must resolve inside a virtio-fs share — workspace or a
	// configured mounts entry. Absolute paths must be under a mounts host
	// path; relative paths are workspace-relative and always inside.
	for name, svc := range c.Services {
		for i, m := range svc.Masks {
			if !maskPathInsideShare(m.Path, c) {
				return fmt.Errorf("services.%s.masks[%d]: path %q is not inside any virtio-fs share (workspace or a mounts entry)", name, i, m.Path)
			}
		}
	}
	return nil
}

// maskPathInsideShare returns true if path resolves inside the
// workspace (relative paths) or under a configured mounts entry
// (absolute paths). Rejects paths that escape via "..".
func maskPathInsideShare(path string, cfg Config) bool {
	if path == "" {
		return false
	}
	cleaned := filepath.Clean(path)
	if strings.HasPrefix(cleaned, "..") {
		return false
	}
	if !filepath.IsAbs(cleaned) {
		// Relative paths are workspace-relative.
		return true
	}
	// Absolute paths must be under a mounts entry's host path.
	for _, m := range cfg.Mounts {
		host := splitMountHost(m)
		if host == "" {
			continue
		}
		cleanedHost := filepath.Clean(host)
		if cleaned == cleanedHost || strings.HasPrefix(cleaned, cleanedHost+"/") {
			return true
		}
	}
	return false
}

func splitMountHost(m string) string {
	if idx := strings.Index(m, ":"); idx >= 0 {
		return m[:idx]
	}
	return m
}
