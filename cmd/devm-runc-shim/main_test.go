package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdubb86/devm/internal/caenv"
)

// helper: minimal valid OCI config.json with a rootfs directory
// that contains the destination file. Returns the bundle dir.
func mkBundle(t *testing.T, includeBundleTarget bool) string {
	t.Helper()
	bundle := t.TempDir()
	rootfs := filepath.Join(bundle, "rootfs")
	if err := os.MkdirAll(filepath.Join(rootfs, "etc/ssl/certs"), 0755); err != nil {
		t.Fatal(err)
	}
	if includeBundleTarget {
		if err := os.WriteFile(
			filepath.Join(rootfs, "etc/ssl/certs/ca-certificates.crt"),
			[]byte("stub"), 0644,
		); err != nil {
			t.Fatal(err)
		}
	}
	spec := map[string]any{
		"ociVersion": "1.0.0",
		"root":       map[string]any{"path": rootfs},
		"mounts":     []any{},
	}
	body, _ := json.MarshalIndent(spec, "", "  ")
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func readMounts(t *testing.T, bundle string) []map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(body, &spec); err != nil {
		t.Fatal(err)
	}
	rawMounts, _ := spec["mounts"].([]any)
	out := make([]map[string]any, 0, len(rawMounts))
	for _, m := range rawMounts {
		out = append(out, m.(map[string]any))
	}
	return out
}

func TestInjectCA_HappyPath_AppendsMount(t *testing.T) {
	bundle := mkBundle(t, true)
	if err := injectCA(bundle); err != nil {
		t.Fatalf("injectCA: %v", err)
	}
	mounts := readMounts(t, bundle)
	if len(mounts) != 1 {
		t.Fatalf("mounts: want 1, got %d", len(mounts))
	}
	m := mounts[0]
	if m["source"] != "/etc/ssl/certs/ca-certificates.crt" {
		t.Errorf("source: want /etc/ssl/certs/ca-certificates.crt, got %v", m["source"])
	}
	if m["destination"] != "/etc/ssl/certs/ca-certificates.crt" {
		t.Errorf("destination: want /etc/ssl/certs/ca-certificates.crt, got %v", m["destination"])
	}
	opts, _ := m["options"].([]any)
	if len(opts) != 2 || opts[0] != "bind" || opts[1] != "ro" {
		t.Errorf(`options: want ["bind","ro"], got %v`, opts)
	}
}

func TestInjectCA_Idempotent_NoDuplicate(t *testing.T) {
	bundle := mkBundle(t, true)
	if err := injectCA(bundle); err != nil {
		t.Fatal(err)
	}
	if err := injectCA(bundle); err != nil {
		t.Fatal(err)
	}
	mounts := readMounts(t, bundle)
	if len(mounts) != 1 {
		t.Errorf("mounts: want 1 (idempotent), got %d", len(mounts))
	}
}

func TestInjectCA_RootfsMissingTarget_SkipsMount(t *testing.T) {
	bundle := mkBundle(t, false) // rootfs exists but ca-certificates.crt does NOT
	if err := injectCA(bundle); err != nil {
		t.Fatalf("injectCA: want nil error on rootfs-probe skip, got %v", err)
	}
	mounts := readMounts(t, bundle)
	if len(mounts) != 0 {
		t.Errorf("mounts: want 0 (skipped for distroless), got %d", len(mounts))
	}
}

func TestInjectCA_MalformedJSON_ReturnsError(t *testing.T) {
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), []byte("{garbage"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := injectCA(bundle); err == nil {
		t.Errorf("injectCA: want error on malformed JSON, got nil")
	}
}

func TestBundleFromArgs_FindsFlag(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"space-separated", []string{"create", "--bundle", "/foo", "id"}, "/foo"},
		{"equals-form", []string{"create", "--bundle=/foo", "id"}, "/foo"},
		{"with-globals", []string{"--systemd-cgroup", "--root", "/x", "create", "--bundle", "/foo", "id"}, "/foo"},
		{"absent", []string{"create", "id"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bundleFromArgs(tc.argv)
			if got != tc.want {
				t.Errorf("bundleFromArgs(%v): want %q, got %q", tc.argv, tc.want, got)
			}
		})
	}
}

// helper: minimal spec with process.env already populated.
func mkBundleWithProcessEnv(t *testing.T, env []string) string {
	t.Helper()
	bundle := t.TempDir()
	rootfs := filepath.Join(bundle, "rootfs")
	if err := os.MkdirAll(filepath.Join(rootfs, "etc/ssl/certs"), 0755); err != nil {
		t.Fatal(err)
	}
	envAny := make([]any, len(env))
	for i, e := range env {
		envAny[i] = e
	}
	spec := map[string]any{
		"ociVersion": "1.0.0",
		"root":       map[string]any{"path": rootfs},
		"process":    map[string]any{"env": envAny},
		"mounts":     []any{},
	}
	body, _ := json.MarshalIndent(spec, "", "  ")
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func readProcessEnv(t *testing.T, bundle string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(body, &spec); err != nil {
		t.Fatal(err)
	}
	process, _ := spec["process"].(map[string]any)
	envAny, _ := process["env"].([]any)
	out := make([]string, 0, len(envAny))
	for _, e := range envAny {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestInjectEnvVars_AppendsAllMissingCaenvVars(t *testing.T) {
	bundle := mkBundleWithProcessEnv(t, []string{"PATH=/usr/bin"})
	if err := injectEnvVars(bundle); err != nil {
		t.Fatalf("injectEnvVars: %v", err)
	}
	env := readProcessEnv(t, bundle)

	// Pre-existing entry preserved.
	found := false
	for _, e := range env {
		if e == "PATH=/usr/bin" {
			found = true
		}
	}
	if !found {
		t.Errorf("PATH=/usr/bin dropped by injectEnvVars; got %v", env)
	}

	// Every caenv.Vars entry present.
	for _, v := range caenv.Vars {
		want := v.Key + "=" + v.Value
		found := false
		for _, e := range env {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q in process.env; got %v", want, env)
		}
	}
}

func TestInjectEnvVars_UserSetKeyPreserved(t *testing.T) {
	userSet := "REQUESTS_CA_BUNDLE=/user/custom.pem"
	bundle := mkBundleWithProcessEnv(t, []string{userSet})
	if err := injectEnvVars(bundle); err != nil {
		t.Fatal(err)
	}
	env := readProcessEnv(t, bundle)

	// User's entry preserved.
	found := false
	for _, e := range env {
		if e == userSet {
			found = true
		}
	}
	if !found {
		t.Errorf("user-set REQUESTS_CA_BUNDLE dropped; got %v", env)
	}

	// No second REQUESTS_CA_BUNDLE=... entry from caenv.
	count := 0
	for _, e := range env {
		if strings.HasPrefix(e, "REQUESTS_CA_BUNDLE=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 REQUESTS_CA_BUNDLE entry, got %d; env=%v", count, env)
	}
}

func TestInjectEnvVars_Idempotent(t *testing.T) {
	bundle := mkBundleWithProcessEnv(t, []string{})
	if err := injectEnvVars(bundle); err != nil {
		t.Fatal(err)
	}
	first := readProcessEnv(t, bundle)
	if err := injectEnvVars(bundle); err != nil {
		t.Fatal(err)
	}
	second := readProcessEnv(t, bundle)
	if len(first) != len(second) {
		t.Errorf("second injectEnvVars must be no-op: first=%d entries, second=%d entries", len(first), len(second))
	}
}

func TestInjectEnvVars_MissingProcess_ReturnsError(t *testing.T) {
	bundle := t.TempDir()
	spec := map[string]any{"ociVersion": "1.0.0"}
	body, _ := json.MarshalIndent(spec, "", "  ")
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
	err := injectEnvVars(bundle)
	if err == nil {
		t.Errorf("injectEnvVars on spec without .process: want error, got nil")
		return
	}
	if !strings.Contains(err.Error(), "process") {
		t.Errorf("error should mention 'process'; got %v", err)
	}
}

func TestSubcmd_ExtractsCreate(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"create", "--bundle", "/foo", "id"}, "create"},
		{[]string{"--systemd-cgroup", "create", "--bundle", "/foo", "id"}, "create"},
		{[]string{"--root", "/x", "--log", "/y", "run", "id"}, "run"},
		{[]string{"delete", "id"}, "delete"},
		{[]string{"--version"}, ""},
		{[]string{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := subcmd(tc.argv)
			if got != tc.want {
				t.Errorf("subcmd(%v): want %q, got %q", tc.argv, tc.want, got)
			}
		})
	}
}
