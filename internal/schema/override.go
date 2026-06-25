package schema

import "gopkg.in/yaml.v3"

type ProjectOverride struct {
	ID          *string `yaml:"id,omitempty"`
	SandboxName *string `yaml:"sandbox_name,omitempty"`
	PortOffset  *int    `yaml:"port_offset,omitempty"`
	Proxy       *string `yaml:"proxy,omitempty"`
}

type BaseImageOverride struct {
	Docker *bool `yaml:"docker,omitempty"`
}

type NetworkOverride struct {
	AllowedDomains *[]string `yaml:"allowed_domains,omitempty"`
}

type ServiceOverride struct {
	// Port + BindIP are populated polymorphically by UnmarshalYAML:
	//   - `port: 80` → Port=&80, BindIP=nil
	//   - `port: "0.0.0.0:80"` → Port=&80, BindIP=&"0.0.0.0"
	// PortIsSet distinguishes "field absent in override" from "set to 0".
	Port      *int    `yaml:"-"`
	BindIP    *string `yaml:"-"`
	PortIsSet bool    `yaml:"-"`

	Hostname  *string           `yaml:"hostname,omitempty"`
	EnvInject *bool             `yaml:"env_inject,omitempty"`
	EnvHost   *string           `yaml:"env_host,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Masks     *[]Mask           `yaml:"masks,omitempty"`
	Templates *[]Template       `yaml:"templates,omitempty"`
	Startup   *[]StartupCommand `yaml:"startup,omitempty"`
}

type serviceOverrideYAML struct {
	Port      yaml.Node         `yaml:"port,omitempty"`
	Hostname  *string           `yaml:"hostname,omitempty"`
	EnvInject *bool             `yaml:"env_inject,omitempty"`
	EnvHost   *string           `yaml:"env_host,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Masks     *[]Mask           `yaml:"masks,omitempty"`
	Templates *[]Template       `yaml:"templates,omitempty"`
	Startup   *[]StartupCommand `yaml:"startup,omitempty"`
}

func (o *ServiceOverride) UnmarshalYAML(node *yaml.Node) error {
	var raw serviceOverrideYAML
	if err := node.Decode(&raw); err != nil {
		return err
	}
	o.Hostname = raw.Hostname
	o.EnvInject = raw.EnvInject
	o.EnvHost = raw.EnvHost
	o.Env = raw.Env
	o.Masks = raw.Masks
	o.Templates = raw.Templates
	o.Startup = raw.Startup
	if raw.Port.Kind == 0 {
		return nil
	}
	o.PortIsSet = true
	var tmp Service
	if err := tmp.decodePortNode(raw.Port); err != nil {
		return err
	}
	o.Port = &tmp.Port
	if tmp.BindIP != "" {
		o.BindIP = &tmp.BindIP
	}
	return nil
}

type ConfigOverride struct {
	Project   *ProjectOverride           `yaml:"project,omitempty"`
	BaseImage *BaseImageOverride         `yaml:"base_image,omitempty"`
	Network   *NetworkOverride           `yaml:"network,omitempty"`
	Env       map[string]string          `yaml:"env,omitempty"`
	Services  map[string]ServiceOverride `yaml:"services,omitempty"`
	Install   *[]string                  `yaml:"install,omitempty"`
	Mounts    *[]string                  `yaml:"mounts,omitempty"`
	Path      *[]string                  `yaml:"path,omitempty"`
}
