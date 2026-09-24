package scriptfile

import (
	"fmt"
	"sort"
	"strings"
)

// WrapperInput drives RenderWrapper. FunctionName is required; the
// zero value of every other field is a valid "no op" (no env exports,
// no PATH prepend, no cd).
type WrapperInput struct {
	FunctionName string
	Env          map[string]string
	PathPrepend  []string
	Cwd          string
}

const (
	guestDevmSH   = "/home/devm/devm.sh"
	guestDevmMeSH = "/home/devm/devm.me.sh"
)

// RenderWrapper produces a bash script body that sources the project's
// devm.sh + devm.me.sh (if present) and invokes in.FunctionName, with
// env exports and an optional cwd bracketed around the call.
func RenderWrapper(in WrapperInput) (string, error) {
	if strings.TrimSpace(in.FunctionName) == "" {
		return "", fmt.Errorf("render wrapper: FunctionName is required")
	}
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -eo pipefail\n")

	if len(in.Env) > 0 {
		keys := make([]string, 0, len(in.Env))
		for k := range in.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "export %s=\"%s\"\n", k, bashEscape(in.Env[k]))
		}
	}
	if len(in.PathPrepend) > 0 {
		fmt.Fprintf(&b, "export PATH=\"%s:$PATH\"\n", strings.Join(in.PathPrepend, ":"))
	}

	fmt.Fprintf(&b, "source %s\n", guestDevmSH)
	fmt.Fprintf(&b, "[ -f %s ] && source %s\n", guestDevmMeSH, guestDevmMeSH)

	if in.Cwd != "" {
		fmt.Fprintf(&b, "cd \"%s\"\n", in.Cwd)
	}

	fmt.Fprintf(&b, "%s\n", in.FunctionName)
	return b.String(), nil
}

// bashEscape prepares a value for a double-quoted bash string literal:
// backslash-escape `"` and `$`. That's enough for env values that hold
// arbitrary bytes; there is no shell-command interpretation intended.
func bashEscape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, `$`, `\$`)
	return v
}
