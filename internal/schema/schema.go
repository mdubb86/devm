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
