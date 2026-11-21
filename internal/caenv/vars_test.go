package caenv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVars_KeysUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range Vars {
		assert.False(t, seen[v.Key], "duplicate key %q", v.Key)
		seen[v.Key] = true
	}
}

func TestVars_ValuesNonEmpty(t *testing.T) {
	for _, v := range Vars {
		assert.NotEmpty(t, v.Value, "%s value must be non-empty", v.Key)
	}
}

// Per-library CA-file env vars must point at the merged bundle. If a
// future entry points at /usr/local/share/ca-certificates/devm.crt
// instead, SSL libs would trust only devm's CA and reject every
// public cert — the trap documented in recipes/lang/uv.md.
func TestVars_CAFileEnvsPointAtBundle(t *testing.T) {
	caFileEnvs := []string{
		"SSL_CERT_FILE",
		"REQUESTS_CA_BUNDLE",
		"CURL_CA_BUNDLE",
		"AWS_CA_BUNDLE",
		"NODE_EXTRA_CA_CERTS",
		"HTTPLIB2_CA_CERTS",
		"GRPC_DEFAULT_SSL_ROOTS_FILE_PATH",
		"GIT_SSL_CAINFO",
		"CARGO_HTTP_CAINFO",
		"PIP_CERT",
	}
	byKey := map[string]string{}
	for _, v := range Vars {
		byKey[v.Key] = v.Value
	}
	for _, key := range caFileEnvs {
		val, ok := byKey[key]
		if assert.True(t, ok, "%s missing from Vars", key) {
			assert.Equal(t, bundlePath, val, "%s must point at %s", key, bundlePath)
		}
	}
}
