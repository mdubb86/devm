// Package image manages the Tart base-image build pipeline. The
// daemon calls BuildBaseImage during `devm install` / `devm upgrade`
// when NeedsBuild returns true.
package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// BaseImageName is the Tart VM name we build into.
const BaseImageName = "devm-base"

// definitionFiles are the inputs to DefinitionHash. Order matters for
// reproducibility; we sort before hashing so additions don't shuffle
// existing entries.
var definitionFiles = []string{
	"build.sh",
	"provision-base.sh",
	"devm-dns.service",
	"devm-caddy.service",
	"README.md",
}

// DefinitionHash returns sha256 over the image definition's inputs.
// imageDir is the path to the `image/` directory in the repo (or
// wherever the daemon has unpacked it at runtime).
func DefinitionHash(imageDir string) (string, error) {
	sorted := append([]string(nil), definitionFiles...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, name := range sorted {
		path := filepath.Join(imageDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		// Name + null + content + null — name embedded so renames
		// change the hash too.
		fmt.Fprintf(h, "%s", name)
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashStorePath returns the disk location where we cache the hash of
// the most recently built image.
func HashStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "devm",
		"cache", "base-image.hash"), nil
}

// NeedsBuild returns (true, currentHash, nil) if the base image
// should be (re)built. False with nil err means up-to-date.
//
// Returns true if any of:
//   - The image definition hash has changed since last build
//   - The devm-base Tart VM is absent from local cache
func NeedsBuild(imageDir string) (bool, string, error) {
	cur, err := DefinitionHash(imageDir)
	if err != nil {
		return false, "", err
	}

	storePath, err := HashStorePath()
	if err != nil {
		return false, "", err
	}
	stored, _ := os.ReadFile(storePath)
	if strings.TrimSpace(string(stored)) != cur {
		return true, cur, nil
	}

	// Hash matches; verify the VM still exists in Tart's cache.
	if !baseImageExists() {
		return true, cur, nil
	}
	return false, cur, nil
}

// baseImageExists is true if `tart list` shows the devm-base VM.
// Returns false on any error reading from Tart (we'd rather rebuild
// than skip a potentially-needed rebuild).
func baseImageExists() bool {
	cmd := exec.Command("tart", "list", "--format=json")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// Cheap substring scan — we just need to know the VM is listed
	// somewhere in the output by name. JSON format on Tart varies a
	// bit across versions; we don't try to fully decode.
	return strings.Contains(string(out), `"`+BaseImageName+`"`)
}

// BuildBaseImage runs imageDir/build.sh. Streams build output to w.
// On success, writes the current definition hash to HashStorePath.
//
// Implementer note: the build can take several minutes on first run
// (template pull + provisioning). Caller is expected to surface
// progress to the user (e.g., by passing os.Stdout as w).
func BuildBaseImage(ctx context.Context, imageDir string, w io.Writer) error {
	hash, err := DefinitionHash(imageDir)
	if err != nil {
		return err
	}

	scriptPath := filepath.Join(imageDir, "build.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("build.sh not found at %s: %w", scriptPath, err)
	}

	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	cmd.Dir = imageDir
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build.sh: %w", err)
	}

	storePath, err := HashStorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(storePath), 0700); err != nil {
		return fmt.Errorf("create hash cache dir: %w", err)
	}
	if err := os.WriteFile(storePath, []byte(hash), 0644); err != nil {
		return fmt.Errorf("write hash: %w", err)
	}
	return nil
}

// ImageDirFromRepoRoot returns repoRoot/image. Helper for callers
// that have the repo root handy.
func ImageDirFromRepoRoot(repoRoot string) string {
	return filepath.Join(repoRoot, "image")
}

// ImageDirFromExe returns the path to the image/ directory relative
// to the running devm binary. Handles two layouts:
//
//  1. Brew / go install: binary at <prefix>/bin/devm, image at
//     <prefix>/share/devm/image (set up by goreleaser).
//  2. Dev: binary at workspace/devm or ./devm, image at
//     workspace/image (next to the source).
//
// Returns the first candidate whose build.sh exists.
func ImageDirFromExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	exeDir := filepath.Dir(exe)

	candidates := []string{
		// Brew/installed layout: <prefix>/bin/devm → <prefix>/share/devm/image
		filepath.Join(exeDir, "..", "share", "devm", "image"),
		// Dev layout: ./devm → ./image
		filepath.Join(exeDir, "image"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "build.sh")); err == nil {
			return c, nil
		}
	}
	// Last resort: cwd/image (handy in dev when running from source root).
	if cwd, err := os.Getwd(); err == nil {
		c := filepath.Join(cwd, "image")
		if _, err := os.Stat(filepath.Join(c, "build.sh")); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("image/ directory not found near devm binary")
}
