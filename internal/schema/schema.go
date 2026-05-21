package schema

import "fmt"

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
}

func (s Service) Validate() error {
	if s.EnvHost != "" && !s.EnvInject {
		return fmt.Errorf("env_host requires env_inject: true")
	}
	if s.EnvInject && s.Canonical == 0 {
		return fmt.Errorf("env_inject requires canonical port")
	}
	if s.Canonical == 0 && len(s.Masks) == 0 {
		return fmt.Errorf("service must define a canonical port or at least one mask")
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
}

func (c Config) Validate() error {
	if err := c.Project.Validate(); err != nil {
		return err
	}
	seenHosts := make(map[string]string)
	seenPorts := make(map[int]string)
	for name, svc := range c.Services {
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
			if prev, ok := seenPorts[svc.Canonical]; ok {
				return fmt.Errorf("duplicate canonical port %d in services %s and %s", svc.Canonical, prev, name)
			}
			seenPorts[svc.Canonical] = name
		}
	}
	return nil
}
