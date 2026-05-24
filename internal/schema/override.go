package schema

type ProjectOverride struct {
	ID           *string `yaml:"id,omitempty"`
	SandboxName  *string `yaml:"sandbox_name,omitempty"`
	HostnameApex *string `yaml:"hostname_apex,omitempty"`
	PortOffset   *int    `yaml:"port_offset,omitempty"`
}

type BaseImageOverride struct {
	Docker *bool `yaml:"docker,omitempty"`
}

type NetworkOverride struct {
	AllowedDomains *[]string `yaml:"allowed_domains,omitempty"`
}

type ServiceOverride struct {
	Canonical *int              `yaml:"canonical,omitempty"`
	Hostname  *string           `yaml:"hostname,omitempty"`
	EnvInject *bool             `yaml:"env_inject,omitempty"`
	EnvHost   *string           `yaml:"env_host,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Masks     *[]Mask           `yaml:"masks,omitempty"`
	Templates *[]Template       `yaml:"templates,omitempty"`
	Startup   *[]StartupCommand `yaml:"startup,omitempty"`
}

type ConfigOverride struct {
	Project   *ProjectOverride           `yaml:"project,omitempty"`
	BaseImage *BaseImageOverride         `yaml:"base_image,omitempty"`
	Network   *NetworkOverride           `yaml:"network,omitempty"`
	Env       map[string]string          `yaml:"env,omitempty"`
	Services  map[string]ServiceOverride `yaml:"services,omitempty"`
	Install   *[]string                  `yaml:"install,omitempty"`
}
