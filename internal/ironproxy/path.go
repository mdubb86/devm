// Package ironproxy locates the bundled iron-proxy binary.
package ironproxy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Path returns the absolute path to the iron-proxy binary, resolving
// via the same fallback chain as image.ImageDirFromExe:
//  1. next-to-devm-binary (dev layout: ./bin/devm → ./bin/iron-proxy)
//  2. <prefix>/share/devm/bin/iron-proxy (brew/installed layout)
//  3. $PATH (last-resort escape hatch for devs who installed iron-proxy themselves)
func Path() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if p, err := pathFromDir(filepath.Dir(exe)); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("iron-proxy"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("iron-proxy not found — re-install devm")
}

// pathFromDir checks the two known layouts relative to exeDir.
func pathFromDir(exeDir string) (string, error) {
	candidates := []string{
		filepath.Join(exeDir, "iron-proxy"),
		filepath.Join(exeDir, "..", "share", "devm", "bin", "iron-proxy"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("iron-proxy not found next to %s", exeDir)
}
