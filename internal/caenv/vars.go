// Package caenv is the single source of truth for CA-trust env vars
// devm exports — both into the guest's /etc/environment (RenderEtcEnvironment)
// and into containers via devm-runc-shim's OCI `process.env` mutation.
// Adding a new entry here reaches both call sites automatically.
package caenv

// bundlePath is the merged system trust store — Mozilla's set plus
// devm's CA, produced by update-ca-certificates. Every CA-file env
// var points here so the same value works both in the guest and
// inside containers (where devm-runc-shim bind-mounts the same
// bundle at the same path).
//
// Never point CA envs at /usr/local/share/ca-certificates/devm.crt —
// that path only exists in the guest, and for SSL_CERT_FILE /
// REQUESTS_CA_BUNDLE it REPLACES the trust set with just one cert
// (see recipes/lang/uv.md: "The SSL_CERT_FILE trap").
const bundlePath = "/etc/ssl/certs/ca-certificates.crt"

// Var is one env-var entry: the exact KEY devm exports and the
// VALUE it exports it to.
type Var struct {
	Key   string
	Value string
}

// Vars is the ordered list devm exports. Emit order in
// /etc/environment matches this slice — keep it stable so diffs
// stay clean.
//
// Entries fall into two groups:
//
//  1. Values pointing at bundlePath — CA-file env vars honored by
//     one specific library or CLI that ignores the general
//     SSL_CERT_FILE / REQUESTS_CA_BUNDLE knobs (e.g. gRPC hardcodes
//     GRPC_DEFAULT_SSL_ROOTS_FILE_PATH; httplib2 checks
//     HTTPLIB2_CA_CERTS before falling back to certifi; git,
//     cargo, and pip each have their own).
//
//  2. Values that aren't a bundle path — NO_PROXY=* tells everything
//     to skip HTTP proxy vars (iron-proxy is transparent, not an
//     HTTP proxy); UV_SYSTEM_CERTS=1 opts uv into the system store;
//     SSL_CERT_DIR points at the hashed-symlink dir.
//
// New additions: only add when we can point at a widely-used tool
// that honors the env var and that isn't already covered by the
// existing knobs.
var Vars = []Var{
	{Key: "NO_PROXY", Value: "*"},
	{Key: "SSL_CERT_FILE", Value: bundlePath},
	{Key: "SSL_CERT_DIR", Value: "/etc/ssl/certs"},
	{Key: "REQUESTS_CA_BUNDLE", Value: bundlePath},
	{Key: "CURL_CA_BUNDLE", Value: bundlePath},
	{Key: "AWS_CA_BUNDLE", Value: bundlePath},
	{Key: "NODE_EXTRA_CA_CERTS", Value: bundlePath},
	{Key: "UV_SYSTEM_CERTS", Value: "1"},
	{Key: "HTTPLIB2_CA_CERTS", Value: bundlePath},
	{Key: "GRPC_DEFAULT_SSL_ROOTS_FILE_PATH", Value: bundlePath},
	{Key: "GIT_SSL_CAINFO", Value: bundlePath},
	{Key: "CARGO_HTTP_CAINFO", Value: bundlePath},
	{Key: "PIP_CERT", Value: bundlePath},
}
