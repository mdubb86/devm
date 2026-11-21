package schema

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSSHTargets_PatternAndUniqueness(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		wantErr string // substring; "" means expect ok
	}{
		{"ok_simple", []string{"prod", "home"}, ""},
		{"ok_with_dot_dash_under", []string{"ops.example.com", "build-box", "my_host"}, ""},
		{"empty_entry", []string{"prod", ""}, "ssh[1]: empty alias"},
		{"starts_with_dash", []string{"-foo"}, "ssh[0]"},
		{"starts_with_underscore", []string{"_foo"}, "ssh[0]"},
		{"too_long", []string{strings.Repeat("a", 254)}, "ssh[0]"},
		{"duplicate", []string{"prod", "prod"}, "ssh[1]: duplicate"},
		{"bad_chars", []string{"has space"}, "ssh[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := append([]string(nil), tc.in...)
			cfg := Config{Project: Project{Name: "p"}, SSH: &list}
			err := cfg.validateSSHTargets()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestResolveSSHTargets_Parses(t *testing.T) {
	out := map[string][]byte{
		"prod": []byte("hostname ops.example.com\nport 22\nuser deploy\nproxyjump \n"),
		"home": []byte("hostname home.net\nport 2222\nuser me\nproxyjump \n"),
	}
	run := func(name string, args ...string) ([]byte, error) {
		if name != "ssh" || len(args) != 2 || args[0] != "-G" {
			t.Fatalf("unexpected exec: %s %v", name, args)
		}
		b, ok := out[args[1]]
		if !ok {
			return nil, fmt.Errorf("alias %q not in fixture", args[1])
		}
		return b, nil
	}
	got, err := ResolveSSHTargets([]string{"prod", "home"}, run)
	if err != nil {
		t.Fatal(err)
	}
	want := []ResolvedSSHTarget{
		{Alias: "prod", HostName: "ops.example.com", Port: 22, User: "deploy"},
		{Alias: "home", HostName: "home.net", Port: 2222, User: "me"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestResolveSSHTargets_RejectsProxyJump(t *testing.T) {
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte("hostname ops.example.com\nport 22\nuser deploy\nproxyjump bastion\n"), nil
	}
	_, err := ResolveSSHTargets([]string{"prod"}, run)
	if err == nil || !strings.Contains(err.Error(), "proxyjump") {
		t.Fatalf("want proxyjump error, got %v", err)
	}
}

func TestResolveSSHTargets_RejectsEmptyHostName(t *testing.T) {
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte("hostname \nport 22\nuser deploy\nproxyjump \n"), nil
	}
	_, err := ResolveSSHTargets([]string{"prod"}, run)
	if err == nil || !strings.Contains(err.Error(), "hostname") {
		t.Fatalf("want hostname error, got %v", err)
	}
}

func TestResolveSSHTargets_RejectsEmptyUser(t *testing.T) {
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte("hostname ops.example.com\nport 22\nuser \nproxyjump \n"), nil
	}
	_, err := ResolveSSHTargets([]string{"prod"}, run)
	if err == nil || !strings.Contains(err.Error(), "user") {
		t.Fatalf("want user error, got %v", err)
	}
}

func TestResolveSSHTargets_SurfacesExecError(t *testing.T) {
	run := func(_ string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("No such alias")
	}
	_, err := ResolveSSHTargets([]string{"ghost"}, run)
	if err == nil || !strings.Contains(err.Error(), `alias "ghost"`) {
		t.Fatalf("want alias error naming the alias, got %v", err)
	}
}

func TestResolveSSHTargets_ParsesHostKeyAlias(t *testing.T) {
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte(
			"hostname 127.0.0.1\n" +
				"port 22\n" +
				"user me\n" +
				"hostkeyalias devm-foo\n",
		), nil
	}
	got, err := ResolveSSHTargets([]string{"devm-foo"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].HostKeyAlias != "devm-foo" {
		t.Fatalf("HostKeyAlias = %q, want devm-foo", got[0].HostKeyAlias)
	}
}

func TestResolveSSHTargets_ParsesUserKnownHostsFile(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	run := func(_ string, _ ...string) ([]byte, error) {
		return []byte(
			"hostname 127.0.0.1\n" +
				"port 22\n" +
				"user me\n" +
				`userknownhostsfile "/a/b" ~/known_hosts` + "\n",
		), nil
	}
	got, err := ResolveSSHTargets([]string{"x"}, run)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/a/b", filepath.Join(home, "known_hosts")}
	if !reflect.DeepEqual(got[0].UserKnownHostsFiles, want) {
		t.Fatalf("UserKnownHostsFiles = %+v, want %+v", got[0].UserKnownHostsFiles, want)
	}
}

// TestCheckKnownHosts_HitsByHostKeyAlias exercises the full lookup
// path end-to-end: write a real known_hosts file keyed by an alias,
// point the target at that file via UserKnownHostsFiles + HostKeyAlias,
// and dispatch to the real `ssh-keygen` binary.
func TestCheckKnownHosts_HitsByHostKeyAlias(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	// A synthetic ed25519 line is fine — ssh-keygen -F only parses
	// the first field for the lookup match.
	line := "devm-foo ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFAKE_ED25519_KEY\n"
	if err := os.WriteFile(kh, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}
	err := CheckKnownHosts([]ResolvedSSHTarget{{
		Alias:               "foo",
		HostName:            "127.0.0.1",
		Port:                22,
		User:                "me",
		HostKeyAlias:        "devm-foo",
		UserKnownHostsFiles: []string{kh},
	}}, run)
	if err != nil {
		t.Fatalf("want ok, got %v", err)
	}
}

func TestCheckKnownHosts_NoFileHitsErrorsLoudly(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(kh, []byte("# empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}
	err := CheckKnownHosts([]ResolvedSSHTarget{{
		Alias:               "foo",
		HostName:            "127.0.0.1",
		HostKeyAlias:        "devm-missing",
		UserKnownHostsFiles: []string{kh},
	}}, run)
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"foo", "devm-missing", "ssh foo", kh} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("want error containing %q, got %v", want, err)
		}
	}
}

// TestCheckKnownHosts_FallsBackToDefaultFilesWhenUnset pins that an
// empty UserKnownHostsFiles probes the default `~/.ssh/known_hosts` +
// `~/.ssh/known_hosts2` pair (with $HOME pointed at a tempdir so the
// real user's files don't leak in).
func TestCheckKnownHosts_FallsBackToDefaultFilesWhenUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kh := filepath.Join(sshDir, "known_hosts")
	line := "ops.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFAKE_ED25519_KEY\n"
	if err := os.WriteFile(kh, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}
	err := CheckKnownHosts([]ResolvedSSHTarget{{
		Alias:    "prod",
		HostName: "ops.example.com",
		Port:     22,
		User:     "deploy",
	}}, run)
	if err != nil {
		t.Fatalf("want ok, got %v", err)
	}
}

func TestCheckKnownHosts_RejectsMissingWithHelpfulMessage(t *testing.T) {
	run := func(_ string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("exit status 1")
	}
	err := CheckKnownHosts([]ResolvedSSHTarget{
		{Alias: "prod", HostName: "ops.example.com", Port: 22, User: "deploy",
			UserKnownHostsFiles: []string{"/nonexistent/known_hosts"}},
	}, run)
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"prod", "ops.example.com", "ssh prod"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("want error containing %q, got %v", want, err)
		}
	}
}

func TestLookupKeyAndFiles(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		in        ResolvedSSHTarget
		wantKey   string
		wantFiles []string
	}{
		{
			"alias_and_files_set",
			ResolvedSSHTarget{HostName: "h", HostKeyAlias: "a", UserKnownHostsFiles: []string{"/a", "/b"}},
			"a", []string{"/a", "/b"},
		},
		{
			"alias_set_files_default",
			ResolvedSSHTarget{HostName: "h", HostKeyAlias: "a"},
			"a", []string{
				filepath.Join(home, ".ssh", "known_hosts"),
				filepath.Join(home, ".ssh", "known_hosts2"),
			},
		},
		{
			"no_alias_falls_back_to_host",
			ResolvedSSHTarget{HostName: "h"},
			"h", []string{
				filepath.Join(home, ".ssh", "known_hosts"),
				filepath.Join(home, ".ssh", "known_hosts2"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, f := LookupKeyAndFiles(tc.in)
			if k != tc.wantKey {
				t.Fatalf("key = %q, want %q", k, tc.wantKey)
			}
			if !reflect.DeepEqual(f, tc.wantFiles) {
				t.Fatalf("files = %+v, want %+v", f, tc.wantFiles)
			}
		})
	}
}
