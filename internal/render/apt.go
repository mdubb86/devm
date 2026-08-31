package render

// AptRetryHelper returns the bash function definition for `apt_run` — a
// wrapper that adds Acquire::Retries=3 (per-file retry inside apt) and
// an outer retry-with-backoff loop around any apt-get invocation. Emit
// once per bash script and call as `apt_run update -y` or
// `apt_run install -y pkg…`.
//
// Backoff schedule across three total attempts: attempt 1 → 5s wait →
// attempt 2 → 15s wait → attempt 3 → fail.
//
// Absorbs the "Transport became inactive" class of transient mirror
// failures without tearing down the VM (field-observed on Shelfmates,
// 2026-08-30 feedback §4).
func AptRetryHelper() string {
	return `apt_run() {
  local attempt
  for attempt in 1 2 3; do
    if sudo apt-get -o Acquire::Retries=3 -o DPkg::Lock::Timeout=60 "$@"; then
      return 0
    fi
    if [ "$attempt" = 3 ]; then
      echo "apt-get $* failed after 3 attempts" >&2
      return 1
    fi
    local sleep_secs=$((attempt * 5))
    echo "apt-get $* attempt $attempt failed, retrying in ${sleep_secs}s..." >&2
    sleep "$sleep_secs"
  done
}
`
}
