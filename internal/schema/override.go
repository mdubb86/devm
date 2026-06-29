package schema

import "gopkg.in/yaml.v3"

type ProjectOverride struct {
	ID     *string `yaml:"id,omitempty"`
	VMName *string `yaml:"vm_name,omitempty"`
	Proxy  *string `yaml:"proxy,omitempty"`
}

type NetworkOverride struct {
	Allow *[]AllowEntry `yaml:"allow,omitempty"`
}

type ServiceOverride struct {
	// Port + BindIP are populated polymorphically by UnmarshalYAML:
	//   - `port: 80` → Port=&80, BindIP=nil
	//   - `port: "0.0.0.0:80"` → Port=&80, BindIP=&"0.0.0.0"
	// PortIsSet distinguishes "field absent in override" from "set to 0".
	Port      *int    `yaml:"-"`
	BindIP    *string `yaml:"-"`
	PortIsSet bool    `yaml:"-"`

	Hostname  *string              `yaml:"hostname,omitempty"`
	Env       map[string]EnvValue  `yaml:"env,omitempty"`
	Masks     *[]Mask              `yaml:"masks,omitempty"`
	Templates *[]Template          `yaml:"templates,omitempty"`
	Exec      *[]string            `yaml:"exec,omitempty"`
	WorkDir   *string              `yaml:"workdir,omitempty"`
	Restart   *string              `yaml:"restart,omitempty"`
	After     *[]string            `yaml:"after,omitempty"`
	User      *string              `yaml:"user,omitempty"`
	Systemd   *string              `yaml:"systemd,omitempty"`
}

type serviceOverrideYAML struct {
	Port      yaml.Node            `yaml:"port,omitempty"`
	Hostname  *string              `yaml:"hostname,omitempty"`
	Env       map[string]EnvValue  `yaml:"env,omitempty"`
	Masks     *[]Mask              `yaml:"masks,omitempty"`
	Templates *[]Template          `yaml:"templates,omitempty"`
	Exec      *[]string            `yaml:"exec,omitempty"`
	WorkDir   *string              `yaml:"workdir,omitempty"`
	Restart   *string              `yaml:"restart,omitempty"`
	After     *[]string            `yaml:"after,omitempty"`
	User      *string              `yaml:"user,omitempty"`
	Systemd   *string              `yaml:"systemd,omitempty"`
}

func (o *ServiceOverride) UnmarshalYAML(node *yaml.Node) error {
	var raw serviceOverrideYAML
	if err := node.Decode(&raw); err != nil {
		return err
	}
	o.Hostname = raw.Hostname
	o.Env = raw.Env
	o.Masks = raw.Masks
	o.Templates = raw.Templates
	o.Exec = raw.Exec
	o.WorkDir = raw.WorkDir
	o.Restart = raw.Restart
	o.After = raw.After
	o.User = raw.User
	o.Systemd = raw.Systemd
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
	Project  *ProjectOverride           `yaml:"project,omitempty"`
	Network  *NetworkOverride           `yaml:"network,omitempty"`
	Env      map[string]EnvValue        `yaml:"env,omitempty"`
	Services map[string]ServiceOverride `yaml:"services,omitempty"`
	Install  *[]string                  `yaml:"install,omitempty"`
	Mounts   *[]string                  `yaml:"mounts,omitempty"`
	Path     *[]string                  `yaml:"path,omitempty"`
	Packages *[]string                  `yaml:"packages,omitempty"`
}
