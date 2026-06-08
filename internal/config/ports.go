package config

import "github.com/mdubb86/devm/internal/schema"

// VMPortOffset is a fixed constant: how much we shift host-published ports
// above their VM-internal counterparts so canonicals stay free on the Mac.
const VMPortOffset = 1000

// BindPort returns where a service actually binds on its host (Mac or VM).
func BindPort(cfg schema.Config, canonical int) int {
	return canonical + cfg.Project.PortOffset
}

// HostPort returns the Mac-side port that sbx publishes the VM service at.
func HostPort(cfg schema.Config, canonical int) int {
	return canonical + VMPortOffset + cfg.Project.PortOffset
}
