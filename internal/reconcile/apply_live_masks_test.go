package reconcile

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildMaskAddScript_MountsBind(t *testing.T) {
	script := buildMaskAddScript("myproj", "node_modules", "/Users/me/workspace/myproj")
	assert.Contains(t, script, "sudo mkdir -p '/var/devm/masks/myproj/node_modules'")
	assert.Contains(t, script, "sudo chown devm:devm '/var/devm/masks/myproj/node_modules'")
	assert.Contains(t, script, "sudo mkdir -p '/Users/me/workspace/myproj/node_modules'")
	assert.Contains(t, script, "sudo mount --bind '/var/devm/masks/myproj/node_modules' '/Users/me/workspace/myproj/node_modules'")
}

func TestBuildMaskAddScript_IdempotentOnRepeat(t *testing.T) {
	// The script must be safe to re-run — mountpoint guard on the
	// target path short-circuits if the bind is already there.
	script := buildMaskAddScript("myproj", "node_modules", "/Users/me/workspace/myproj")
	assert.Contains(t, script, "mountpoint -q '/Users/me/workspace/myproj/node_modules'")
}

func TestBuildMaskAddScript_NestedPath(t *testing.T) {
	// Nested masks like companion/.venv exercise mkdir -p behavior.
	script := buildMaskAddScript("myproj", "companion/.venv", "/Users/me/workspace/myproj")
	assert.Contains(t, script, "/var/devm/masks/myproj/companion/.venv")
	assert.Contains(t, script, "/Users/me/workspace/myproj/companion/.venv")
}

func TestBuildMaskRemoveScript_Umounts(t *testing.T) {
	script := buildMaskRemoveScript("node_modules", "/Users/me/workspace/myproj")
	assert.Contains(t, script, "sudo umount '/Users/me/workspace/myproj/node_modules'")
}

func TestBuildMaskRemoveScript_IsIdempotent(t *testing.T) {
	// If already unmounted, the script must exit clean (mountpoint -q
	// guard) rather than error.
	script := buildMaskRemoveScript("node_modules", "/Users/me/workspace/myproj")
	assert.Contains(t, script, "mountpoint -q '/Users/me/workspace/myproj/node_modules'")
}

func TestBuildMaskAddScript_QuotesPathsWithMetachars(t *testing.T) {
	// Path containing shell metachars — schema allows spaces + ;.
	script := buildMaskAddScript("myproj", "with space", "/wsroot")
	// The mount target substring must appear in a quoted form.
	assert.Contains(t, script, `'/wsroot/with space'`)
	// And the storage path likewise.
	assert.Contains(t, script, `'/var/devm/masks/myproj/with space'`)
}
