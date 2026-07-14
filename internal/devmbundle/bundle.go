package devmbundle

import (
	"archive/tar"
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/mdubb86/devm/internal/render"
	"github.com/mdubb86/devm/internal/schema"
	"github.com/mdubb86/devm/internal/scripts"
)

// zeroTime is a deterministic mtime for every tar header. Fixed value
// so two builds of the same cfg produce byte-identical archives.
var zeroTime = time.Unix(0, 0).UTC()

// BuildInput carries the inputs devmbundle.Build needs. Fields grow
// as more artifacts fold into the bundle; existing fields are stable.
type BuildInput struct {
	Cfg            schema.Config
	RepoRoot       string
	CARootPEM      []byte
	DockerRuncShim []byte
	DockerCLIShim  []byte
}

// Build returns a tar archive containing the devm-owned artifacts the
// guest needs at /opt/devm/. The daemon pipes this into the guest via
// PipeIntoShell + GuestInstallScript.
func Build(in BuildInput) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	envBody, err := render.RenderEnv(in.Cfg)
	if err != nil {
		return nil, fmt.Errorf("render env: %w", err)
	}
	if err := writeEntry(tw, ".env", 0o644, []byte(envBody)); err != nil {
		return nil, err
	}
	if err := writeEntry(tw, "scripts/with-devm-env", 0o755, []byte(scripts.WithDevmEnv)); err != nil {
		return nil, err
	}
	if err := writeEntry(tw, "scripts/install-templates.sh", 0o755, []byte(scripts.InstallTemplates)); err != nil {
		return nil, err
	}

	templates, err := render.RenderTemplates(in.Cfg, in.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("render templates: %w", err)
	}
	// Deterministic ordering: sort keys before writing.
	names := sortedKeys(templates)
	for _, name := range names {
		if err := writeEntry(tw, "templates/"+filepath.Base(name), 0o755, []byte(templates[name])); err != nil {
			return nil, err
		}
	}

	if err := writeEntry(tw, "install.sh", 0o755, []byte(scripts.Install)); err != nil {
		return nil, err
	}

	if len(in.CARootPEM) > 0 {
		if err := writeEntry(tw, "ca/devm.crt", 0o644, in.CARootPEM); err != nil {
			return nil, err
		}
	}

	if in.Cfg.Docker {
		if len(in.DockerRuncShim) == 0 || len(in.DockerCLIShim) == 0 {
			return nil, fmt.Errorf("Cfg.Docker=true requires DockerRuncShim and DockerCLIShim bytes")
		}
		if err := writeEntry(tw, "bin/devm-runc-shim", 0o755, in.DockerRuncShim); err != nil {
			return nil, err
		}
		if err := writeEntry(tw, "bin/docker", 0o755, in.DockerCLIShim); err != nil {
			return nil, err
		}
	}

	caddyfile := render.Caddyfile(in.Cfg)
	if err := writeEntry(tw, "caddy/Caddyfile", 0o644, []byte(caddyfile)); err != nil {
		return nil, err
	}

	if err := writeEntry(tw, "dnsmasq/devm-test.conf", 0o644, render.DnsmasqConfig()); err != nil {
		return nil, err
	}

	// One systemd unit per service that will actually run in-guest.
	// Routing-only services (no Exec, no Systemd) contribute proxy config
	// only and don't get a unit; matches enableStartServices' skip logic.
	svcNames := make([]string, 0, len(in.Cfg.Services))
	for name := range in.Cfg.Services {
		svcNames = append(svcNames, name)
	}
	sort.Strings(svcNames)
	for _, name := range svcNames {
		svc := in.Cfg.Services[name]
		if svc.Systemd == "" && len(svc.Exec) == 0 {
			continue
		}
		// Merge top-level env into per-service env so cfg.Env entries
		// (including !secret refs) reach the rendered systemd unit.
		// Per-service env wins on key collision — explicit beats inherited.
		merged := make(map[string]schema.EnvValue, len(in.Cfg.Env)+len(svc.Env))
		for k, v := range in.Cfg.Env {
			merged[k] = v
		}
		for k, v := range svc.Env {
			merged[k] = v
		}
		svc.Env = merged
		unit := render.RenderService(name, svc)
		if err := writeEntry(tw, "systemd/"+name+".service", 0o644, unit); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeEntry(tw *tar.Writer, name string, mode int64, body []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(body)),
		ModTime: zeroTime,
		Uid:     0,
		Gid:     0,
		Format:  tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("write tar body %s: %w", name, err)
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Simple insertion sort; templates are small.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
