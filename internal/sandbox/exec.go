package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/mdubb86/devm/internal/schema"
)

// Exec runs `sbx exec [-it] [-w workdir] -e ... NAME with-devm-env args...`.
// The with-devm-env wrapper sources /.devm/.env so the executed command
// sees devm's persistent project + service env. When interactive is true,
// hooks stdin/stdout/stderr; otherwise captures output into the returned
// slices.
func (s *Sandbox) Exec(cfg schema.Config, repoRoot string, args []string, interactive bool, workdir string) (capturedStdout, capturedStderr []byte, err error) {
	wrapper := filepath.Join(repoRoot, ".devm", "scripts", "with-devm-env")
	full := []string{"exec"}
	if interactive {
		full = append(full, "-it")
		full = append(full, EnvArgs(cfg)...)
	}
	if workdir != "" {
		full = append(full, "-w", workdir)
	}
	full = append(full, s.Name, wrapper)
	full = append(full, args...)

	cmd := exec.Command("sbx", full...)
	if interactive {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Run()
		return nil, nil, err
	}
	capturedStdout, err = cmd.Output()
	if exitErr, ok := err.(*exec.ExitError); ok {
		capturedStderr = exitErr.Stderr
	}
	return
}
