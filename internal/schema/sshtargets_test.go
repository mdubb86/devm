package schema

import (
	"fmt"
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

func TestCheckKnownHosts_Allows(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		if name == "ssh-keygen" && len(args) == 2 && args[0] == "-F" && args[1] == "ops.example.com" {
			return []byte("# Host ops.example.com found: line 1\nops.example.com ssh-ed25519 AAAA...\n"), nil
		}
		return nil, fmt.Errorf("not in known_hosts")
	}
	err := CheckKnownHosts([]ResolvedSSHTarget{
		{Alias: "prod", HostName: "ops.example.com", Port: 22, User: "deploy"},
	}, run)
	if err != nil {
		t.Fatalf("want ok, got %v", err)
	}
}

func TestCheckKnownHosts_RejectsMissingWithHelpfulMessage(t *testing.T) {
	run := func(_ string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("exit status 1")
	}
	err := CheckKnownHosts([]ResolvedSSHTarget{
		{Alias: "prod", HostName: "ops.example.com", Port: 22, User: "deploy"},
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
