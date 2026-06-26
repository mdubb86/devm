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
		if override.Project.Proxy != nil {
			out.Project.Proxy = *override.Project.Proxy
		}
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
			if soverride.Exec != nil {
				svc.Exec = *soverride.Exec
			}
			if soverride.WorkDir != nil {
				svc.WorkDir = *soverride.WorkDir
			}
			if soverride.Restart != nil {
				svc.Restart = *soverride.Restart
			}
			if soverride.After != nil {
				svc.After = *soverride.After
			}
			if soverride.User != nil {
				svc.User = *soverride.User
			}
			if soverride.Systemd != nil {
				svc.Systemd = *soverride.Systemd
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
	if override.Path != nil {
		out.Path = *override.Path
	}
	if override.Packages != nil {
		out.Packages = *override.Packages
	}
	return out, nil
}
