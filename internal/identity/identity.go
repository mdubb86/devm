// Package identity holds the daemon's compile-time identity — the set
// of values that differ between the prod devm install and the
// parallel e2e install (devm-e2e). Selected at binary startup by a
// single ldflag: -X github.com/mdubb86/devm/internal/identity.Profile=<prod|e2e>.
//
// Every place in the codebase that today reads a hardcoded identity
// constant (launchd label, TLD, runtime dir, pool range, CA CN, base
// image name) reads from Config instead. There is exactly one call to
// Load() per process, in cmd/devm/main.go and cmd/devm-helper/main.go.
package identity

import (
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Config is the daemon's compile-time identity. Values are held
// on the struct directly for fields that don't derive; methods
// return derived values (e.g. LaunchdLabelDaemon builds
// "com.<name-with-hyphens-as-dots>.service" from Name).
type Config struct {
	Name             string // "devm" | "devm-e2e"
	TLD              string // "test" | "e2e.test"
	ResolverFilePath string // /etc/resolver/<tld>
	DNSBindAddr      string // "127.0.0.1:51153" | "127.0.0.1:51154"
	PoolStart        int    // lo0 alias pool low bound
	PoolEnd          int    // lo0 alias pool high bound (inclusive)

	// HelperSocketPath is the root helper's UDS. Root-owned; mode
	// 0660, group c.GroupName(). Requesting clients must be in that
	// group. A stored field (not derived from Name like GroupName/
	// BaseImageName/Launchd*) so tests can point a Client at a
	// scratch UDS instead of the real root-owned prod/e2e path.
	HelperSocketPath string // "/var/run/devm-helper.sock" | "/var/run/devm-e2e-helper.sock"
}

// Profile selects between Prod and E2E at load time. Default value
// "prod" is overridden at build time via:
//
//	-ldflags "-X github.com/mdubb86/devm/internal/identity.Profile=e2e"
var Profile = "prod"

// Prod is the identity the shipped devm binary uses.
var Prod = Config{
	Name:             "devm",
	TLD:              "test",
	ResolverFilePath: "/etc/resolver/test",
	DNSBindAddr:      "127.0.0.1:51153",
	PoolStart:        1,
	PoolEnd:          20,
	HelperSocketPath: "/var/run/devm-helper.sock",
}

// E2E is the identity the devm-e2e binary uses. Coexists with Prod on
// the same Mac without collision — different plists, different runtime
// dir, different pool range, different TLD, different CA CN.
var E2E = Config{
	Name:             "devm-e2e",
	TLD:              "e2e.test",
	ResolverFilePath: "/etc/resolver/e2e.test",
	DNSBindAddr:      "127.0.0.1:51154",
	PoolStart:        21,
	PoolEnd:          40,
	HelperSocketPath: "/var/run/devm-e2e-helper.sock",
}

// Load returns the Config selected by Profile. Panics on unknown
// profile — that's a build-configuration bug (bad ldflag), not a
// runtime condition.
func Load() Config {
	switch Profile {
	case "prod":
		return Prod
	case "e2e":
		return E2E
	}
	panic("identity.Load: unknown profile: " + Profile)
}

// RuntimeDir is where the daemon persists per-user state (socket,
// iron-proxy configs, CA material, project state snapshots).
// ~/Library/Application Support/<Name>/. On errors reading the home
// dir, returns just the leaf under an empty string — install-time
// callers will fail loudly at file ops rather than silently writing
// to the wrong place.
func (c Config) RuntimeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", c.Name)
}

// SocketPath is the daemon's Unix domain socket path.
func (c Config) SocketPath() string {
	return filepath.Join(c.RuntimeDir(), "devm.sock")
}

// GroupName is the Unix group that gates access to the helper UDS.
// Created at install time via dscl.
func (c Config) GroupName() string {
	return "_" + c.Name
}

// CACommonName is the x509 Subject CN of the daemon's local CA cert.
// The `security` command differentiates prod vs. e2e trust chains
// by this CN in the shared system keychain.
func (c Config) CACommonName() string {
	return c.Name + " Local CA"
}

// BaseImageName is the tart VM image name the daemon clones project
// VMs from. Prod and e2e have separate base images so e2e's base-
// lifecycle tests can rebuild/wipe freely.
func (c Config) BaseImageName() string {
	return c.Name + "-base"
}

// LaunchdLabelDaemon is the reverse-DNS label for the main daemon's
// launchd job. "com." + name-with-hyphens-as-dots + ".service".
// Prod: com.devm.service; e2e: com.devm.e2e.service.
func (c Config) LaunchdLabelDaemon() string {
	return "com." + strings.ReplaceAll(c.Name, "-", ".") + ".service"
}

// LaunchdLabelHelper is the label for the root helper's launchd job.
func (c Config) LaunchdLabelHelper() string {
	return "com." + strings.ReplaceAll(c.Name, "-", ".") + ".helper"
}

// LaunchdPlistDaemon is the on-disk plist path for the main daemon.
func (c Config) LaunchdPlistDaemon() string {
	return "/Library/LaunchDaemons/" + c.LaunchdLabelDaemon() + ".plist"
}

// LaunchdPlistHelper is the on-disk plist path for the helper.
func (c Config) LaunchdPlistHelper() string {
	return "/Library/LaunchDaemons/" + c.LaunchdLabelHelper() + ".plist"
}

// LaunchdTargetDaemon is the argument for launchctl bootstrap/bootout
// operating on the main daemon.
func (c Config) LaunchdTargetDaemon() string {
	return "system/" + c.LaunchdLabelDaemon()
}

// LaunchdTargetHelper is the argument for launchctl bootstrap/bootout
// operating on the helper.
func (c Config) LaunchdTargetHelper() string {
	return "system/" + c.LaunchdLabelHelper()
}

// CanonicalResolverContents returns the exact bytes written to the
// per-TLD resolver file. macOS reads /etc/resolver/<TLD> and forwards
// queries matching that suffix to (nameserver, port).
func (c Config) CanonicalResolverContents() string {
	host, port, _ := net.SplitHostPort(c.DNSBindAddr)
	return "nameserver " + host + "\nport " + port + "\n"
}

// HelperBinaryPath returns the install-time extraction path for the
// embedded helper binary. Derived from os.Executable(): the helper is
// installed side-by-side with the daemon binary, named
// <basename>-helper. Works uniformly whether the daemon is at
// /usr/local/bin/devm, /opt/homebrew/bin/devm, or bin/devm-e2e in a
// repo checkout.
func HelperBinaryPath() string {
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), filepath.Base(exe)+"-helper")
}
