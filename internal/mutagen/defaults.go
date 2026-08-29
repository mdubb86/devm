package mutagen

// DefaultIgnores is the daemon-supplied ignore prefix applied to every
// mutagen sync session. Composed with the user's per-entry `ignore:`
// list at session-create time (defaults first, user last, so user can
// un-ignore any of these via mutagen's `!pattern` negation).
var DefaultIgnores = []string{
	".git/objects/pack/", // root-only; only one .git at sync root
	"**/node_modules/",
	"**/.venv/", "**/venv/",
	"**/__pycache__/",
	"*.pyc",
	"**/.DS_Store",
	"**/dist/", "**/build/",
	"**/.next/cache/",
	"**/.turbo/",
	"**/.pytest_cache/", "**/.mypy_cache/",
}
