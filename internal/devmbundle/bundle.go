package devmbundle

import (
	"archive/tar"
	"bytes"
	"fmt"
	"path/filepath"
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
	Cfg       schema.Config
	RepoRoot  string
	CARootPEM []byte
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

	caddyfile := render.Caddyfile(in.Cfg)
	if err := writeEntry(tw, "caddy/Caddyfile", 0o644, []byte(caddyfile)); err != nil {
		return nil, err
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
