package render

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"text/template"

	"golang.org/x/crypto/bcrypt"

	"github.com/mdubb86/devm/internal/scripts"
)

// RenderInstallScript substitutes {{.MutagenVersion}} and the bundled
// filestash blobs in scripts.InstallTemplate and returns the rendered
// install.sh body. Called per-project at bundle-build time: the
// filestash admin password is bcrypt-hashed from projectName so the
// browser prompt accepts the project's own name, not a shared constant.
func RenderInstallScript(mutagenVersion, projectName string) ([]byte, error) {
	if mutagenVersion == "" {
		return nil, fmt.Errorf("render install script: mutagen version is required")
	}
	if projectName == "" {
		return nil, fmt.Errorf("render install script: project name is required")
	}
	cfgBody, err := filestashConfigForProject(projectName)
	if err != nil {
		return nil, fmt.Errorf("render install script: filestash config: %w", err)
	}
	t, err := template.New("install.sh").Parse(scripts.InstallTemplate)
	if err != nil {
		return nil, fmt.Errorf("render install script: parse template: %w", err)
	}
	data := struct {
		MutagenVersion     string
		FilestashBinaryB64 string
		FilestashConfigB64 string
		FilestashUnit      string
	}{
		MutagenVersion:     mutagenVersion,
		FilestashBinaryB64: base64.StdEncoding.EncodeToString(scripts.EmbeddedFilestashBinary),
		FilestashConfigB64: base64.StdEncoding.EncodeToString(cfgBody),
		FilestashUnit:      scripts.EmbeddedFilestashSystemdUnit,
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render install script: execute template: %w", err)
	}
	return buf.Bytes(), nil
}

// filestashConfigForProject loads the embedded filestash preset and swaps
// its auth.admin bcrypt hash for one derived from projectName, so each
// guest's filestash accepts its own project name at the password prompt.
// The signed middleware blobs and secret_key stay as baked — those bind
// the passthrough identity provider to the local backend, not to any
// particular viewer, so they remain project-agnostic.
//
// Cached per projectName: bcrypt uses a random salt so successive calls
// would otherwise produce different hashes, breaking the devmbundle
// determinism contract (two Build calls over the same cfg must yield
// byte-identical tars so re-pipe can gate on content hash). The cache is
// process-local and single-user, so size and eviction aren't concerns.
var filestashConfigCache sync.Map // projectName (string) -> []byte

func filestashConfigForProject(projectName string) ([]byte, error) {
	if cached, ok := filestashConfigCache.Load(projectName); ok {
		return cached.([]byte), nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(scripts.EmbeddedFilestashConfig, &cfg); err != nil {
		return nil, fmt.Errorf("parse embedded config: %w", err)
	}
	auth, ok := cfg["auth"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("embedded config: auth block missing or wrong shape")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(projectName), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("bcrypt project name: %w", err)
	}
	auth["admin"] = string(hash)
	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("re-marshal config: %w", err)
	}
	filestashConfigCache.Store(projectName, out)
	return out, nil
}
