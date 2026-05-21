package sandbox

import (
	"os"
	"os/exec"

	"github.com/mtwaage/devm/internal/schema"
)

// Exec runs `sbx exec [-it] [-w workdir] -e ... NAME args...`. When interactive
// is true, hooks stdin/stdout/stderr; otherwise captures output into the
// returned slices.
func (s *Sandbox) Exec(cfg schema.Config, args []string, interactive bool, workdir string) (capturedStdout, capturedStderr []byte, err error) {
	full := []string{"exec"}
	if interactive {
		full = append(full, "-it")
		full = append(full, EnvArgs(cfg)...)
	}
	if workdir != "" {
		full = append(full, "-w", workdir)
	}
	full = append(full, s.Name)
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
