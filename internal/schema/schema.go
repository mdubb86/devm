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

type StartupCommand struct {
	Command    []string `yaml:"command"`
	Background bool     `yaml:"background,omitempty"`
}

func (c StartupCommand) Validate() error {
	if len(c.Command) == 0 {
		return fmt.Errorf("startup.command must be a non-empty array")
	}
	for i, arg := range c.Command {
		if arg == "" {
			return fmt.Errorf("startup.command[%d] must not be empty", i)
		}
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
	//
	// The host port is NOT overridable. devm guarantees
	// `host_port = port_offset + port` for every service. The bind
	// portion of `port: "IP:N"` controls only the interface; the
	// trailing N must equal the sandbox port.
	BindIP string `yaml:"-"`

	Hostname  string            `yaml:"hostname,omitempty"`
	EnvInject bool              `yaml:"env_inject,omitempty"`
	EnvHost   string            `yaml:"env_host,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Masks     []Mask            `yaml:"masks,omitempty"`
	Templates []Template        `yaml:"templates,omitempty"`
	Startup   []StartupCommand  `yaml:"startup,omitempty"`
}

// serviceYAML is the on-the-wire shape. `port` is a yaml.Node so we
// can decode it as either int or string and populate both Service.Port
// and Service.BindIP from a single field.
type serviceYAML struct {
	Port      yaml.Node         `yaml:"port,omitempty"`
	Hostname  string            `yaml:"hostname,omitempty"`
	EnvInject bool              `yaml:"env_inject,omitempty"`
	EnvHost   string            `yaml:"env_host,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Masks     []Mask            `yaml:"masks,omitempty"`
	Templates []Template        `yaml:"templates,omitempty"`
	Startup   []StartupCommand  `yaml:"startup,omitempty"`
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
	s.EnvInject = raw.EnvInject
	s.EnvHost = raw.EnvHost
	s.Env = raw.Env
	s.Masks = raw.Masks
	s.Templates = raw.Templates
	s.Startup = raw.Startup
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
		Port      interface{}       `yaml:"port,omitempty"`
		Hostname  string            `yaml:"hostname,omitempty"`
		EnvInject bool              `yaml:"env_inject,omitempty"`
		EnvHost   string            `yaml:"env_host,omitempty"`
		Env       map[string]string `yaml:"env,omitempty"`
		Masks     []Mask            `yaml:"masks,omitempty"`
		Templates []Template        `yaml:"templates,omitempty"`
		Startup   []StartupCommand  `yaml:"startup,omitempty"`
	}{
		Hostname:  s.Hostname,
		EnvInject: s.EnvInject,
		EnvHost:   s.EnvHost,
		Env:       s.Env,
		Masks:     s.Masks,
		Templates: s.Templates,
		Startup:   s.Startup,
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
	if s.EnvHost != "" && !s.EnvInject {
		return fmt.Errorf("env_host requires env_inject: true")
	}
	if s.EnvInject && s.Port == 0 {
		return fmt.Errorf("env_inject requires port")
	}
	if s.BindIP != "" && s.Port == 0 {
		return fmt.Errorf("port bind interface requires a sandbox port")
	}
	if s.Port == 0 && len(s.Masks) == 0 && len(s.Startup) == 0 {
		return fmt.Errorf("service must define a port, at least one mask, or at least one startup command")
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
	for i, c := range s.Startup {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("startup[%d]: %w", i, err)
		}
	}
	return nil
}

type Project struct {
	ID           string `yaml:"id"`
	SandboxName  string `yaml:"sandbox_name"`
	HostnameApex string `yaml:"hostname_apex"`
	PortOffset   int    `yaml:"port_offset,omitempty"`
}

func (p Project) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("project.id is required")
	}
	if p.SandboxName == "" {
		return fmt.Errorf("project.sandbox_name is required")
	}
	if p.HostnameApex == "" {
		return fmt.Errorf("project.hostname_apex is required")
	}
	return nil
}

type BaseImage struct {
	Docker bool `yaml:"docker"`
}

type Network struct {
	AllowedDomains []string `yaml:"allowed_domains,omitempty"`
}

type Config struct {
	Project   Project            `yaml:"project"`
	BaseImage BaseImage          `yaml:"base_image"`
	Network   Network            `yaml:"network,omitempty"`
	Env       map[string]string  `yaml:"env,omitempty"`
	Services  map[string]Service `yaml:"services,omitempty"`

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

	// Mounts are additional host paths mounted into the sandbox at
	// the same path inside the VM (sbx's "mirrored path" mode). Each
	// entry is a string of the form `HOST_PATH[:ro]`. HOST_PATH may
	// be absolute, relative to the project root, or start with `~`
	// for home-directory expansion. The optional `:ro` suffix is
	// passed through to sbx verbatim and makes the mount read-only.
	//
	// Changing this field is in the TEARDOWN bucket: sbx run's
	// positional workspaces are baked at create time and the sandbox
	// must be removed and re-created to apply.
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
// `ABS_HOST_PATH[:ro]` ready to pass as a positional to `sbx run`.
//
// Rules (matching sbx's own CLI parsing):
//   - Optional `:ro` suffix is preserved.
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
			host := c.Project.PortOffset + svc.Port
			if host > 65535 {
				return fmt.Errorf("services.%s: port_offset %d + canonical %d = %d exceeds max TCP port 65535",
					name, c.Project.PortOffset, svc.Port, host)
			}
			if prev, ok := seenPorts[svc.Port]; ok {
				return fmt.Errorf("duplicate port %d in services %s and %s", svc.Port, prev, name)
			}
			seenPorts[svc.Port] = name
		}
	}
	return nil
}
