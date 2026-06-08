package schema

import (
	"fmt"
	"sort"
	"strings"
)

// reservedEnvKeys are keys devm injects into cfg.Env after expansion.
// Users may not set them in env: or any services[*].env: block.
var reservedEnvKeys = map[string]struct{}{
	"WORKSPACE":  {},
	"IS_SANDBOX": {},
}

// ResolveEnv validates the env maps, expands devm-owned variables in
// values, and injects the system-vars set (WORKSPACE, IS_SANDBOX).
// It mutates cfg in place. On error, cfg is left untouched (no injection).
//
//   - Substitutes $WORKSPACE and ${WORKSPACE} → repoRoot in every value
//     under cfg.Env and cfg.Services[*].Env.
//   - $$ → literal $.
//   - Any other $VAR or ${VAR} reference is an error.
//   - Any reserved key (WORKSPACE, IS_SANDBOX) in cfg.Env or any
//     service env is an error.
func ResolveEnv(cfg *Config, repoRoot string) error {
	// Pre-validate reserved keys before mutation.
	for k := range cfg.Env {
		if _, ok := reservedEnvKeys[k]; ok {
			return fmt.Errorf("env.%s is reserved by devm. Remove this key.", k)
		}
	}
	for svcName, svc := range cfg.Services {
		for k := range svc.Env {
			if _, ok := reservedEnvKeys[k]; ok {
				return fmt.Errorf("services.%s.env.%s is reserved by devm. Remove this key.", svcName, k)
			}
		}
	}

	// Validate + expand cfg.Path into a side buffer.
	resolvedPath := make([]string, 0, len(cfg.Path))
	for i, p := range cfg.Path {
		out, err := expandWorkspace(p, repoRoot)
		if err != nil {
			return fmt.Errorf("path[%d]: %w", i, err)
		}
		if out == "" {
			return fmt.Errorf("path[%d]: empty entry not allowed", i)
		}
		if strings.HasPrefix(out, "~") {
			return fmt.Errorf("path[%d] %q: leading ~ not supported; use $WORKSPACE or an absolute path", i, p)
		}
		if !strings.HasPrefix(out, "/") {
			return fmt.Errorf("path[%d] %q: must be absolute (start with / or $WORKSPACE)", i, p)
		}
		resolvedPath = append(resolvedPath, out)
	}

	// Expand into a side buffer; only commit if all succeed.
	resolved := make(map[string]string, len(cfg.Env)+2)
	for k, v := range cfg.Env {
		out, err := expandWorkspace(v, repoRoot)
		if err != nil {
			return fmt.Errorf("env.%s: %w", k, err)
		}
		resolved[k] = out
	}
	type svcEdit struct {
		name string
		env  map[string]string
	}
	svcEdits := make([]svcEdit, 0, len(cfg.Services))
	svcNames := make([]string, 0, len(cfg.Services))
	for n := range cfg.Services {
		svcNames = append(svcNames, n)
	}
	sort.Strings(svcNames)
	for _, svcName := range svcNames {
		svc := cfg.Services[svcName]
		if len(svc.Env) == 0 {
			continue
		}
		expanded := make(map[string]string, len(svc.Env))
		for k, v := range svc.Env {
			out, err := expandWorkspace(v, repoRoot)
			if err != nil {
				return fmt.Errorf("services.%s.env.%s: %w", svcName, k, err)
			}
			expanded[k] = out
		}
		svcEdits = append(svcEdits, svcEdit{name: svcName, env: expanded})
	}

	// Commit phase: no more failures possible.
	resolved["WORKSPACE"] = repoRoot
	resolved["IS_SANDBOX"] = "1"
	cfg.Env = resolved
	cfg.Path = resolvedPath
	for _, e := range svcEdits {
		svc := cfg.Services[e.name]
		svc.Env = e.env
		cfg.Services[e.name] = svc
	}
	return nil
}

// expandWorkspace performs devm's tiny variable-expansion pass:
//   - $WORKSPACE and ${WORKSPACE} → repoRoot
//   - $$ → literal $
//   - any other $VAR or ${VAR} reference → error
//
// Identifiers follow shell convention: [A-Za-z_][A-Za-z0-9_]*.
func expandWorkspace(v, repoRoot string) (string, error) {
	var out strings.Builder
	out.Grow(len(v))
	i := 0
	for i < len(v) {
		c := v[i]
		if c != '$' {
			out.WriteByte(c)
			i++
			continue
		}
		// At '$'. Peek next.
		if i+1 >= len(v) {
			return "", fmt.Errorf("trailing $ in value %q (use $$ for a literal $)", v)
		}
		next := v[i+1]
		switch {
		case next == '$':
			out.WriteByte('$')
			i += 2
		case next == '{':
			end := strings.IndexByte(v[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated ${ in value %q", v)
			}
			name := v[i+2 : i+2+end]
			if name == "WORKSPACE" {
				out.WriteString(repoRoot)
			} else {
				return "", fmt.Errorf("references unknown variable $%s. devm only expands $WORKSPACE.", name)
			}
			i += 2 + end + 1
		case isIdentStart(next):
			j := i + 1
			for j < len(v) && isIdentCont(v[j]) {
				j++
			}
			name := v[i+1 : j]
			if name == "WORKSPACE" {
				out.WriteString(repoRoot)
			} else {
				return "", fmt.Errorf("references unknown variable $%s. devm only expands $WORKSPACE.", name)
			}
			i = j
		default:
			return "", fmt.Errorf("unexpected character after $ in value %q (use $$ for a literal $)", v)
		}
	}
	return out.String(), nil
}

func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentCont(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}
