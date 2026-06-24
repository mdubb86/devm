package config

import "github.com/mdubb86/devm/internal/schema"

// Merge applies override on top of base. Only non-nil/non-empty fields in
// override take effect. Returns a new Config.
func Merge(base schema.Config, override schema.ConfigOverride) (schema.Config, error) {
	out := base
	if override.Project != nil {
		if override.Project.ID != nil {
			out.Project.ID = *override.Project.ID
		}
		if override.Project.SandboxName != nil {
			out.Project.SandboxName = *override.Project.SandboxName
		}
		if override.Project.PortOffset != nil {
			out.Project.PortOffset = *override.Project.PortOffset
		}
	}
	if override.BaseImage != nil && override.BaseImage.Docker != nil {
		out.BaseImage.Docker = *override.BaseImage.Docker
	}
	if override.Network != nil && override.Network.AllowedDomains != nil {
		out.Network.AllowedDomains = *override.Network.AllowedDomains
	}
	if override.Env != nil {
		if out.Env == nil {
			out.Env = map[string]string{}
		}
		for k, v := range override.Env {
			out.Env[k] = v
		}
	}
	if override.Services != nil {
		if out.Services == nil {
			out.Services = map[string]schema.Service{}
		}
		for name, soverride := range override.Services {
			svc := out.Services[name]
			if soverride.PortIsSet {
				if soverride.Port != nil {
					svc.Port = *soverride.Port
				}
				if soverride.BindIP != nil {
					svc.BindIP = *soverride.BindIP
				} else {
					svc.BindIP = ""
				}
			}
			if soverride.Hostname != nil {
				svc.Hostname = *soverride.Hostname
			}
			if soverride.EnvInject != nil {
				svc.EnvInject = *soverride.EnvInject
			}
			if soverride.EnvHost != nil {
				svc.EnvHost = *soverride.EnvHost
			}
			if soverride.Env != nil {
				if svc.Env == nil {
					svc.Env = map[string]string{}
				}
				for k, v := range soverride.Env {
					svc.Env[k] = v
				}
			}
			if soverride.Masks != nil {
				svc.Masks = *soverride.Masks
			}
			if soverride.Templates != nil {
				svc.Templates = *soverride.Templates
			}
			if soverride.Startup != nil {
				svc.Startup = *soverride.Startup
			}
			out.Services[name] = svc
		}
	}
	if override.Install != nil {
		out.Install = *override.Install
	}
	if override.Mounts != nil {
		out.Mounts = *override.Mounts
	}
	return out, nil
}
