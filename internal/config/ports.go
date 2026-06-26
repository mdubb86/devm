package config

import "github.com/mdubb86/devm/internal/schema"

// BindPort returns where a service actually binds on its host.
// With Tart VMs each VM has its own IP, so the canonical port
// is also the bind port — no offset needed.
func BindPort(_ schema.Config, canonical int) int {
	return canonical
}

// HostPort returns the Mac-side port for a service. With Tart VMs
// the service is accessed at its canonical port on the VM's IP.
func HostPort(_ schema.Config, canonical int) int {
	return canonical
}
