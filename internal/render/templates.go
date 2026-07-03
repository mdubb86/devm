// Package render — template renderer for services.<name>.templates.
// Produces self-contained bash installer scripts (one per template)
// that write the rendered content into the sandbox at the configured
// output path. See internal design notes.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/mdubb86/devm/internal/schema"
)

// TemplateData is the data context exposed to user templates.
type TemplateData struct {
	Project ProjectData
	Service map[string]ServiceData
	Env     map[string]string
}

// ProjectData mirrors schema.Project fields useful to templates.
type ProjectData struct {
	ID     string
	VMName string
}

// ServiceData mirrors schema.Service fields useful to templates plus
// the computed HostPort = Port (Tart VMs have their own IP; canonical
// port == host-visible port on the VM's IP).
type ServiceData struct {
	Port     int
	HostPort int
	Hostname string
	Env      map[string]string
}

// RenderTemplates renders every services.*.templates entry into a map
// of absolute installer-script paths under <repoRoot>/.devm/templates/
// to their shell-script contents. Caller writes them to disk.
//
// Empty result on no templates. Errors on: missing source file, source
// outside repoRoot, undefined template variable, parse errors.
func RenderTemplates(cfg schema.Config, repoRoot string) (map[string]string, error) {
	out := map[string]string{}
	if len(cfg.Services) == 0 {
		return out, nil
	}
	data := buildTemplateData(cfg)
	dir := filepath.Join(repoRoot, ".devm", "templates")

	// Deterministic order: services alphabetically, templates in
	// declaration order. The NN index ensures stable glob iteration.
	svcNames := make([]string, 0, len(cfg.Services))
	for n := range cfg.Services {
		svcNames = append(svcNames, n)
	}
	sort.Strings(svcNames)

	idx := 0
	for _, svc := range svcNames {
		for _, tmpl := range cfg.Services[svc].Templates {
			rendered, err := renderOne(repoRoot, svc, tmpl, data)
			if err != nil {
				return nil, err
			}
			scriptBody := installerScript(tmpl.Output, rendered, tmpl.Sudo)
			base := filepath.Base(tmpl.Output)
			name := fmt.Sprintf("%02d-%s-%s.sh", idx, svc, base)
			out[filepath.Join(dir, name)] = scriptBody
			idx++
		}
	}
	return out, nil
}

func buildTemplateData(cfg schema.Config) TemplateData {
	svcData := make(map[string]ServiceData, len(cfg.Services))
	for name, s := range cfg.Services {
		hostPort := s.Port // Tart VMs have their own IP; canonical == host-visible
		env := make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			env[k] = v.Render()
		}
		svcData[name] = ServiceData{
			Port:     s.Port,
			HostPort: hostPort,
			Hostname: s.Hostname,
			Env:      env,
		}
	}
	pEnv := make(map[string]string, len(cfg.Env))
	for k, v := range cfg.Env {
		pEnv[k] = v.Render()
	}
	return TemplateData{
		Project: ProjectData{
			ID:     cfg.Project.ID,
			VMName: cfg.Project.VMName,
		},
		Service: svcData,
		Env:     pEnv,
	}
}

func renderOne(repoRoot, svc string, tmpl schema.Template, data TemplateData) (string, error) {
	srcAbs := filepath.Join(repoRoot, tmpl.Source)
	cleaned, err := filepath.Abs(srcAbs)
	if err != nil {
		return "", fmt.Errorf("template %s/%s: abs(%s): %w", svc, tmpl.Output, srcAbs, err)
	}
	rootAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("template %s/%s: abs(repoRoot): %w", svc, tmpl.Output, err)
	}
	// Resolve symlinks on BOTH sides so the containment check is on
	// the real filesystem path. filepath.Abs alone won't catch a
	// symlink at the source whose target lives outside the project
	// (or a project root that is itself a symlink, like /tmp on macOS).
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("template %s/%s: eval symlinks for repoRoot %q: %w", svc, tmpl.Output, repoRoot, err)
	}
	resolvedSrc, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("template %s/%s: source %q: %w", svc, tmpl.Output, tmpl.Source, err)
	}
	if !strings.HasPrefix(resolvedSrc, resolvedRoot+string(filepath.Separator)) && resolvedSrc != resolvedRoot {
		return "", fmt.Errorf("template %s/%s: source %q resolves outside project root", svc, tmpl.Output, tmpl.Source)
	}
	raw, err := os.ReadFile(resolvedSrc)
	if err != nil {
		return "", fmt.Errorf("template %s/%s: read source %q: %w", svc, tmpl.Output, tmpl.Source, err)
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("template %s/%s: source %q is empty", svc, tmpl.Output, tmpl.Source)
	}
	t, err := template.New(tmpl.Source).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("template %s/%s: parse %q: %w", svc, tmpl.Output, tmpl.Source, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("template %s/%s: execute %q: %w", svc, tmpl.Output, tmpl.Source, err)
	}
	return b.String(), nil
}

// installerScript builds the per-template bash installer. The heredoc
// delimiter is derived from sha256(content)[:12], salted with a counter
// so we can re-hash if the unlikely event a content line collides with
// the chosen delimiter occurs.
//
// When useSudo is false (the schema default), the installer stages TMP in
// $(dirname DEST) and mv's it into place as the guest user. A failed write
// (DEST is under /etc, /usr, …) bubbles up as a cold-start error so the
// user is told to add `sudo: true` to the template.
//
// When useSudo is true, the installer stages TMP in /tmp (world-writable),
// then `sudo install -m 0644 "$TMP" "$DEST"` lands it root-owned.
func installerScript(dest, content string, useSudo bool) string {
	delim := heredocDelimiter(content, 0)
	for salt := 1; lineMatches(content, delim); salt++ {
		delim = heredocDelimiter(content, salt)
	}
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("# Generated by devm. DO NOT EDIT.\n")
	b.WriteString(fmt.Sprintf("# template -> %s (sudo=%v)\n", dest, useSudo))
	b.WriteString("set -euo pipefail\n")
	b.WriteString(fmt.Sprintf("DEST=%s\n", shellSingleQuoted(dest)))
	if useSudo {
		b.WriteString("TMP=\"$(mktemp)\"\n")
		b.WriteString("trap 'rm -f \"$TMP\"' EXIT\n")
	} else {
		b.WriteString("TMP=\"${DEST}.devm-install-tmp.$$\"\n")
		b.WriteString("mkdir -p \"$(dirname \"$DEST\")\"\n")
	}
	b.WriteString(fmt.Sprintf("cat > \"$TMP\" <<'%s'\n", delim))
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(delim)
	b.WriteString("\n")
	if useSudo {
		// install(1) copies TMP to DEST atomically, sets mode + owner.
		// Root ownership matches what /etc/… config files expect.
		b.WriteString("sudo install -m 0644 -o root -g root \"$TMP\" \"$DEST\"\n")
	} else {
		b.WriteString("chmod 0644 \"$TMP\"\n")
		b.WriteString("mv \"$TMP\" \"$DEST\"\n")
	}
	return b.String()
}

func heredocDelimiter(content string, salt int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", salt, content)))
	return "DEVM_TPL_" + hex.EncodeToString(h[:6])
}

// lineMatches reports whether `content` contains `delim` as a complete
// line (the only thing on a line). If so, that delimiter would terminate
// the heredoc prematurely.
func lineMatches(content, delim string) bool {
	for _, line := range strings.Split(content, "\n") {
		if line == delim {
			return true
		}
	}
	return false
}

// shellSingleQuoted wraps s in single quotes for use as a bash literal.
func shellSingleQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
