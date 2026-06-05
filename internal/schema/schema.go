package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	Canonical int               `yaml:"canonical,omitempty"`
	Hostname  string            `yaml:"hostname,omitempty"`
	EnvInject bool              `yaml:"env_inject,omitempty"`
	EnvHost   string            `yaml:"env_host,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Masks     []Mask            `yaml:"masks,omitempty"`
	Templates []Template        `yaml:"templates,omitempty"`
	Startup   []StartupCommand  `yaml:"startup,omitempty"`
}

func (s Service) Validate() error {
	if s.EnvHost != "" && !s.EnvInject {
		return fmt.Errorf("env_host requires env_inject: true")
	}
	if s.EnvInject && s.Canonical == 0 {
		return fmt.Errorf("env_inject requires canonical port")
	}
	if s.Canonical == 0 && len(s.Masks) == 0 && len(s.Startup) == 0 {
		return fmt.Errorf("service must define a canonical port, at least one mask, or at least one startup command")
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
	Install   []string           `yaml:"install,omitempty"`

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
		if svc.Canonical != 0 {
			if svc.Canonical < 1 || svc.Canonical > 65535 {
				return fmt.Errorf("services.%s: canonical port %d out of range (1-65535)", name, svc.Canonical)
			}
			host := c.Project.PortOffset + svc.Canonical
			if host > 65535 {
				return fmt.Errorf("services.%s: port_offset %d + canonical %d = %d exceeds max TCP port 65535",
					name, c.Project.PortOffset, svc.Canonical, host)
			}
			if prev, ok := seenPorts[svc.Canonical]; ok {
				return fmt.Errorf("duplicate canonical port %d in services %s and %s", svc.Canonical, prev, name)
			}
			seenPorts[svc.Canonical] = name
		}
	}
	return nil
}
