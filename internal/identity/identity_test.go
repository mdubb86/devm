package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProd(t *testing.T) {
	origProfile := Profile
	defer func() { Profile = origProfile }()

	Profile = "prod"
	c := Load()
	if c.Name != "devm" {
		t.Errorf("prod Name = %q, want devm", c.Name)
	}
	if c.TLD != "test" {
		t.Errorf("prod TLD = %q, want test", c.TLD)
	}
	if c.DNSBindAddr != "127.0.0.1:51153" {
		t.Errorf("prod DNSBindAddr = %q", c.DNSBindAddr)
	}
	if c.PoolStart != 1 || c.PoolEnd != 20 {
		t.Errorf("prod pool = %d..%d", c.PoolStart, c.PoolEnd)
	}
}

func TestLoadE2E(t *testing.T) {
	origProfile := Profile
	defer func() { Profile = origProfile }()

	Profile = "e2e"
	c := Load()
	if c.Name != "devm-e2e" {
		t.Errorf("e2e Name = %q, want devm-e2e", c.Name)
	}
	if c.TLD != "e2e.test" {
		t.Errorf("e2e TLD = %q, want e2e.test", c.TLD)
	}
	if c.DNSBindAddr != "127.0.0.1:51154" {
		t.Errorf("e2e DNSBindAddr = %q", c.DNSBindAddr)
	}
	if c.PoolStart != 21 || c.PoolEnd != 40 {
		t.Errorf("e2e pool = %d..%d", c.PoolStart, c.PoolEnd)
	}
}

func TestLoadUnknownProfilePanics(t *testing.T) {
	origProfile := Profile
	defer func() { Profile = origProfile }()

	Profile = "bogus"
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on unknown profile")
		}
	}()
	_ = Load()
}

func TestDerivations(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want map[string]string
	}{
		{"prod", Prod, map[string]string{
			"HelperSocketPath":    "/var/run/devm-helper.sock",
			"GroupName":           "_devm",
			"CACommonName":        "devm Local CA",
			"BaseImageName":       "devm-base",
			"LaunchdLabelDaemon":  "com.devm.service",
			"LaunchdLabelHelper":  "com.devm.helper",
			"LaunchdPlistDaemon":  "/Library/LaunchDaemons/com.devm.service.plist",
			"LaunchdPlistHelper":  "/Library/LaunchDaemons/com.devm.helper.plist",
			"LaunchdTargetDaemon": "system/com.devm.service",
			"LaunchdTargetHelper": "system/com.devm.helper",
		}},
		{"e2e", E2E, map[string]string{
			"HelperSocketPath":    "/var/run/devm-e2e-helper.sock",
			"GroupName":           "_devm-e2e",
			"CACommonName":        "devm-e2e Local CA",
			"BaseImageName":       "devm-e2e-base",
			"LaunchdLabelDaemon":  "com.devm.e2e.service",
			"LaunchdLabelHelper":  "com.devm.e2e.helper",
			"LaunchdPlistDaemon":  "/Library/LaunchDaemons/com.devm.e2e.service.plist",
			"LaunchdPlistHelper":  "/Library/LaunchDaemons/com.devm.e2e.helper.plist",
			"LaunchdTargetDaemon": "system/com.devm.e2e.service",
			"LaunchdTargetHelper": "system/com.devm.e2e.helper",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.cfg
			got := map[string]string{
				"HelperSocketPath":    c.HelperSocketPath,
				"GroupName":           c.GroupName(),
				"CACommonName":        c.CACommonName(),
				"BaseImageName":       c.BaseImageName(),
				"LaunchdLabelDaemon":  c.LaunchdLabelDaemon(),
				"LaunchdLabelHelper":  c.LaunchdLabelHelper(),
				"LaunchdPlistDaemon":  c.LaunchdPlistDaemon(),
				"LaunchdPlistHelper":  c.LaunchdPlistHelper(),
				"LaunchdTargetDaemon": c.LaunchdTargetDaemon(),
				"LaunchdTargetHelper": c.LaunchdTargetHelper(),
			}
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("%s: %s = %q, want %q", tt.name, k, got[k], want)
				}
			}
		})
	}
}

// TestDeleteBaseImageOnUninstall pins the prod/e2e split for spec
// §8.3: e2e's uninstall deletes its base image (its base-lifecycle
// tests want a clean slate); prod's does not (a user's base image is
// expensive to rebuild and shouldn't vanish on uninstall).
func TestDeleteBaseImageOnUninstall(t *testing.T) {
	if Prod.DeleteBaseImageOnUninstall {
		t.Errorf("prod DeleteBaseImageOnUninstall = true, want false")
	}
	if !E2E.DeleteBaseImageOnUninstall {
		t.Errorf("e2e DeleteBaseImageOnUninstall = false, want true")
	}
}

func TestRuntimeDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := Prod.RuntimeDir(); got != filepath.Join(home, "Library", "Application Support", "devm") {
		t.Errorf("prod RuntimeDir = %q", got)
	}
	if got := E2E.RuntimeDir(); got != filepath.Join(home, "Library", "Application Support", "devm-e2e") {
		t.Errorf("e2e RuntimeDir = %q", got)
	}
}

func TestLogDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := Prod.LogDir(); got != filepath.Join(home, "Library", "Logs", "devm") {
		t.Errorf("prod LogDir = %q", got)
	}
	if got := E2E.LogDir(); got != filepath.Join(home, "Library", "Logs", "devm-e2e") {
		t.Errorf("e2e LogDir = %q", got)
	}
}

func TestSocketPath(t *testing.T) {
	if !strings.HasSuffix(Prod.SocketPath(), "/devm/devm.sock") {
		t.Errorf("prod SocketPath suffix wrong: %q", Prod.SocketPath())
	}
	if !strings.HasSuffix(E2E.SocketPath(), "/devm-e2e/devm.sock") {
		t.Errorf("e2e SocketPath suffix wrong: %q", E2E.SocketPath())
	}
}

func TestCanonicalResolverContents(t *testing.T) {
	if got := Prod.CanonicalResolverContents(); got != "nameserver 127.0.0.1\nport 51153\n" {
		t.Errorf("prod resolver:\n%q", got)
	}
	if got := E2E.CanonicalResolverContents(); got != "nameserver 127.0.0.1\nport 51154\n" {
		t.Errorf("e2e resolver:\n%q", got)
	}
}
