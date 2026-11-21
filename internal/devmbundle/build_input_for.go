package devmbundle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mdubb86/devm/internal/docker"
	"github.com/mdubb86/devm/internal/guestbin"
	"github.com/mdubb86/devm/internal/mutagen"
	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/schema"
)

// BuildInputFor assembles the BuildInput for a project. Single
// source for BuildInput field population: any future addition
// (new bundled binary, new env template, new blob) gets added
// here and every bundle-pipe entry point picks it up.
//
// macCwd is the project's Mac-side directory — where devm.yaml
// lives and where devm.sh / devm.me.sh are read from so the
// in-guest snapshot hook runner can source them. Empty macCwd
// (e.g. the drift-refresh path where only the StateSnapshot.Cfg
// is available) leaves the hook-source fields empty.
//
// The paths differ ONLY in which cfg they pass — reconcile passes
// on-disk cfg (approve-gated), RefreshGuestBundle passes
// StateSnapshot.Cfg (last-applied). This helper does not know
// which caller invoked it and does not enforce approve semantics —
// that is the caller's responsibility.
func BuildInputFor(
	cfg schema.Config,
	repoRoot, macCwd, daemonRuntimeDir, tld string,
	caPEM, sshAuthPub, sshHostPriv, sshHostPub []byte,
) (BuildInput, error) {
	commandsManifest, err := render.RenderCommandsManifest(cfg, repoRoot)
	if err != nil {
		return BuildInput{}, fmt.Errorf("render commands manifest: %w", err)
	}
	mutagenAgent, err := mutagen.LinuxArm64Agent()
	if err != nil {
		return BuildInput{}, fmt.Errorf("extract mutagen agent: %w", err)
	}
	devmSh, err := ReadOptionalFile(macCwd, "devm.sh")
	if err != nil {
		return BuildInput{}, fmt.Errorf("read devm.sh: %w", err)
	}
	devmMeSh, err := ReadOptionalFile(macCwd, "devm.me.sh")
	if err != nil {
		return BuildInput{}, fmt.Errorf("read devm.me.sh: %w", err)
	}
	in := BuildInput{
		Cfg:                    cfg,
		RepoRoot:               repoRoot,
		DaemonRuntimeDir:       daemonRuntimeDir,
		TLD:                    tld,
		CARootPEM:              caPEM,
		SSHAuthorizedPubkey:    sshAuthPub,
		SSHHostPriv:            sshHostPriv,
		SSHHostPub:             sshHostPub,
		Gdevm:                  guestbin.Gdevm(),
		CommandsManifest:       commandsManifest,
		MutagenAgentLinuxArm64: mutagenAgent,
		MutagenVersion:         strings.TrimPrefix(mutagen.EmbeddedVersion(), "v"),
		SizingConf:             render.RenderSizingConf(cfg),
		DevmSh:                 devmSh,
		DevmMeSh:               devmMeSh,
	}
	if cfg.DockerEnabled() {
		in.DockerRuncShim = docker.Shim()
		in.DockerCLIShim = docker.DockerShim()
	}
	if cfg.SSH != nil && len(*cfg.SSH) > 0 {
		if SSHEnricher == nil {
			return BuildInput{}, fmt.Errorf("ssh bundle fields: SSHEnricher not bound (daemon wiring bug)")
		}
		f, err := SSHEnricher(cfg, daemonRuntimeDir)
		if err != nil {
			return BuildInput{}, fmt.Errorf("ssh bundle fields: %w", err)
		}
		in.SSHClientConfig = f.ClientConfig
		in.SSHJumpClientPriv = f.JumpClientPriv
		in.SSHKnownHostsAppend = f.KnownHostsAppend
		in.SSHAgentBridgeUnit = f.AgentBridgeUnit
	}
	return in, nil
}

// ReadOptionalFile returns the bytes of dir/name when the file exists,
// nil when it does not, and a real error for anything else. An empty
// dir short-circuits to nil — callers without a macCwd (drift-refresh)
// don't attempt the read. Shared with the cold-start provisioner so
// both paths read the user's devm.sh / devm.me.sh the same way.
func ReadOptionalFile(dir, name string) ([]byte, error) {
	if dir == "" {
		return nil, nil
	}
	body, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return body, nil
}
