package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
