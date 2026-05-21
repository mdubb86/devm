package sandbox

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mtwaage/devm/internal/config"
	"github.com/mtwaage/devm/internal/schema"
)

// ForwardEnv is the host env vars we propagate into every interactive
// sbx exec so TUIs see correct terminal capabilities.
var ForwardEnv = []string{"TERM", "COLORTERM", "LANG", "LC_ALL", "LC_CTYPE"}

// EnvArgs returns the `-e KEY=VALUE` args for sbx exec, combining:
//  1. forwarded host env vars (TERM, COLORTERM, etc.)
//  2. service-port injections (NAME_PORT, NAME_HOST) for services that
//     opted into env_inject
//  3. service-scoped env vars (NAME_KEY = VALUE) flattened with UPPER prefix
//  4. project-wide env vars from cfg.Env
//
// Services whose name starts with "supabase" (case-insensitive) are SKIPPED
// for port injection — those names collide with the supabase CLI's env prefix
// and would silently override the project's supabase config.toml.
func EnvArgs(cfg schema.Config) []string {
	var args []string

	// 1. Forwarded host env vars.
	for _, k := range ForwardEnv {
		if v := os.Getenv(k); v != "" {
			args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
		}
	}

	// 2 + 3. Per-service injections + env. Sort for deterministic output.
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := cfg.Services[name]
		upper := strings.ToUpper(name)
		isSupabasePrefix := strings.HasPrefix(strings.ToLower(name), "supabase")

		if svc.EnvInject && svc.Canonical != 0 && !isSupabasePrefix {
			args = append(args, "-e", fmt.Sprintf("%s_PORT=%d", upper, config.BindPort(cfg, svc.Canonical)))
			if svc.EnvHost != "" {
				args = append(args, "-e", fmt.Sprintf("%s_HOST=%s", upper, svc.EnvHost))
			}
		}

		keys := make([]string, 0, len(svc.Env))
		for k := range svc.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "-e", fmt.Sprintf("%s_%s=%s", upper, k, svc.Env[k]))
		}
	}

	// 4. Project-wide env vars.
	keys := make([]string, 0, len(cfg.Env))
	for k := range cfg.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, cfg.Env[k]))
	}

	return args
}
