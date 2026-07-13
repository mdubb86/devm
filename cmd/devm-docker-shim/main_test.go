package main

import (
	"reflect"
	"testing"
)

func TestShouldInjectSecret(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"plain_build", []string{"build", "."}, true},
		{"plain_build_with_flags", []string{"build", "-t", "foo:latest", "."}, true},
		{"buildx_build", []string{"buildx", "build", "."}, true},
		{"buildx_build_with_flags", []string{"buildx", "build", "--platform", "linux/arm64", "."}, true},
		{"global_flag_then_build", []string{"--context", "default", "build", "."}, true},
		{"global_short_flag_then_build", []string{"-H", "unix:///var/run/docker.sock", "build", "."}, true},
		{"equals_form_flag_then_build", []string{"--log-level=debug", "build", "."}, true},
		{"global_flag_then_buildx_build", []string{"--context", "default", "buildx", "build", "."}, true},
		{"run_no_inject", []string{"run", "alpine", "sh"}, false},
		{"pull_no_inject", []string{"pull", "alpine"}, false},
		{"version_no_inject", []string{"version"}, false},
		{"info_no_inject", []string{"info"}, false},
		{"buildx_bake_no_inject", []string{"buildx", "bake"}, false},
		{"buildx_ls_no_inject", []string{"buildx", "ls"}, false},
		{"empty_no_inject", []string{}, false},
		{"only_global_flags_no_inject", []string{"--debug"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldInjectSecret(tc.argv); got != tc.want {
				t.Errorf("shouldInjectSecret(%v) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}

func TestFirstPositional(t *testing.T) {
	cases := []struct {
		name  string
		argv  []string
		want  string
		rest  []string
		found bool
	}{
		{"empty", []string{}, "", nil, false},
		{"only_flag", []string{"--debug"}, "", nil, false},
		{"single_positional", []string{"info"}, "info", []string{}, true},
		{"flag_then_positional", []string{"--debug", "info"}, "info", []string{}, true},
		{"valued_flag_skipped", []string{"--context", "default", "build"}, "build", []string{}, true},
		{"equals_flag_not_valued", []string{"--log-level=info", "build"}, "build", []string{}, true},
		{
			"two_positionals_returns_first",
			[]string{"buildx", "build", "."},
			"buildx",
			[]string{"build", "."},
			true,
		},
		{
			"short_valued_flag_skipped",
			[]string{"-H", "unix:///var/run/docker.sock", "ps"},
			"ps",
			[]string{},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rest, found := firstPositional(tc.argv)
			if got != tc.want || found != tc.found || !reflect.DeepEqual(rest, tc.rest) {
				t.Errorf("firstPositional(%v) = (%q, %v, %v), want (%q, %v, %v)",
					tc.argv, got, rest, found, tc.want, tc.rest, tc.found)
			}
		})
	}
}
