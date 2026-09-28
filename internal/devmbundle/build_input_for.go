package devmbundle

import (
	"fmt"
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
// The paths differ ONLY in which cfg they pass — reconcile passes
// on-disk cfg (approve-gated), RefreshGuestBundle passes
// StateSnapshot.Cfg (last-applied). This helper does not know
// which caller invoked it and does not enforce approve semantics —
// that is the caller's responsibility.
func BuildInputFor(
	cfg schema.Config,
	repoRoot, daemonRuntimeDir string,
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
	in := BuildInput{
		Cfg:                    cfg,
		RepoRoot:               repoRoot,
		DaemonRuntimeDir:       daemonRuntimeDir,
		CARootPEM:              caPEM,
		SSHAuthorizedPubkey:    sshAuthPub,
		SSHHostPriv:            sshHostPriv,
		SSHHostPub:             sshHostPub,
		Gdevm:                  guestbin.Gdevm(),
		CommandsManifest:       commandsManifest,
		MutagenAgentLinuxArm64: mutagenAgent,
		MutagenVersion:         strings.TrimPrefix(mutagen.EmbeddedVersion(), "v"),
	}
	if cfg.Docker {
		in.DockerRuncShim = docker.Shim()
		in.DockerCLIShim = docker.DockerShim()
	}
	return in, nil
}
