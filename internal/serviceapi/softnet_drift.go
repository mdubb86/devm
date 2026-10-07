package serviceapi

import (
	"fmt"
	"log"
	"regexp"

	"github.com/mdubb86/devm/internal/softnet"
)

// SoftnetDriftInfo is the drift-report shape surfaced in /vm/status and
// in `devm reconcile` / `devm status` output. nil ⇒ no drift.
type SoftnetDriftInfo struct {
	LocalSHA  string `json:"local_sha"`
	RemoteSHA string `json:"remote_sha"`
	Message   string `json:"message"`
}

var contractSHAShape = regexp.MustCompile(`^[0-9a-f]{64}$`)

// probeSoftnetContract queries softnet for its ContractSHA and validates
// the shape. A non-nil error means drift: the dial failed, the ack timed
// out, OR the response isn't a lowercase 64-char hex string.
func probeSoftnetContract(sock string) (string, error) {
	cli := newSoftnetClient(sock)
	sha, err := cli.getContract()
	if err != nil {
		return "", err
	}
	if !contractSHAShape.MatchString(sha) {
		return "", fmt.Errorf("softnet contract sha malformed: %q", sha)
	}
	return sha, nil
}

// newSoftnetDriftInfo returns nil when no drift. A probe error OR a sha
// mismatch produces a non-nil info with the user-facing Message baked in.
func newSoftnetDriftInfo(projectName, projectDir, localSHA, remoteSHA string, probeErr error) *SoftnetDriftInfo {
	if probeErr == nil && remoteSHA == localSHA {
		return nil
	}
	return &SoftnetDriftInfo{
		LocalSHA:  localSHA,
		RemoteSHA: remoteSHA,
		Message:   formatDriftMessage(projectName, projectDir, localSHA, remoteSHA),
	}
}

// formatDriftMessage is the single source of the restart-instruction
// block. If projectDir is empty, falls back to a generic placeholder so
// copy-paste still parses.
func formatDriftMessage(projectName, projectDir, localSHA, remoteSHA string) string {
	dir := projectDir
	if dir == "" {
		dir = "<your project directory>"
	}
	short := func(s string) string {
		if s == "" {
			return "unreachable"
		}
		if len(s) < 12 {
			return s
		}
		return s[:12]
	}
	return fmt.Sprintf(
		"softnet(%s): subprocess is running an older contract (sha=%s, build=%s).\n"+
			"Restart the VM to pick up the current code:\n"+
			"    cd %s && devm stop && devm start",
		projectName, short(remoteSHA), short(localSHA), dir,
	)
}

// logDriftIfAny probes softnet via sock and emits exactly one log line
// when drift is detected; nothing when subprocess and daemon agree.
func logDriftIfAny(projectID, projectDir, sock string) {
	remoteSHA, probeErr := probeSoftnetContract(sock)
	info := newSoftnetDriftInfo(projectID, projectDir, softnet.ContractSHA, remoteSHA, probeErr)
	if info == nil {
		return
	}
	dir := projectDir
	if dir == "" {
		dir = "<your project directory>"
	}
	log.Printf("softnet-drift: %s: subprocess sha=%q daemon sha=%s (restart: cd %s && devm stop && devm start)",
		projectID, info.RemoteSHA, info.LocalSHA, dir)
}
