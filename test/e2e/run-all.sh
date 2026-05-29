#!/usr/bin/env bash
set -uo pipefail
cd "$(dirname "$0")"

# --- two-layer cleanup: registry + EXIT trap ---
export E2E_REGISTRY="$(mktemp -t devm-e2e-registry.XXXX)"
sweep_registry() {
    local exit_code=$?
    if [ -s "$E2E_REGISTRY" ]; then
        echo ""
        echo "=== sweep_registry: cleaning leaked sandboxes/workspaces/policies ==="
        while IFS=$'\t' read -r a b; do
            [ -z "$a" ] && continue
            if [ "$a" = "policy" ]; then
                echo "  sbx policy rm network --resource $b"
                sbx policy rm network --resource "$b" >/dev/null 2>&1 || true
            else
                echo "  sbx rm $a  +  rm -rf $b"
                sbx rm "$a" >/dev/null 2>&1 || true
                rm -rf "$b"  >/dev/null 2>&1 || true
            fi
        done < "$E2E_REGISTRY"
    fi
    rm -f "$E2E_REGISTRY"
    exit $exit_code
}
trap sweep_registry EXIT INT TERM

# --- build devm binary into a temp location ---
DEVM_BIN="${DEVM_BIN:-$(mktemp -d)/devm}"
if ! (cd ../.. && go build -o "$DEVM_BIN" ./cmd/devm); then
    echo "BUILD FAILED"
    exit 1
fi
export DEVM_BIN

# --- run tests serially (VM-bound; no parallelism in v1) ---
fail_count=0
pass_count=0
suite_start=$(date +%s)
declare -a failed
declare -a timings

for script in $(ls -1 [0-9]*.exp 2>/dev/null | sort); do
    printf "=== %s ===\n" "$script"
    t_start=$(date +%s)
    if expect "$script"; then
        pass_count=$((pass_count + 1))
    else
        fail_count=$((fail_count + 1))
        failed+=("$script")
    fi
    t_elapsed=$(( $(date +%s) - t_start ))
    timings+=("$(printf '%-40s %3ds' "$script" "$t_elapsed")")
    printf -- "--- %s: %ds ---\n\n" "$script" "$t_elapsed"
done
suite_elapsed=$(( $(date +%s) - suite_start ))

# --- report ---
echo "==============================================="
printf "Per-test timing:\n"
for t in "${timings[@]}"; do
    printf "  %s\n" "$t"
done
printf "Total: %ds\n" "$suite_elapsed"
printf "Results: %d passed, %d failed\n" "$pass_count" "$fail_count"
if [ $fail_count -gt 0 ]; then
    printf "Failed:\n"
    for f in "${failed[@]}"; do
        printf "  - %s\n" "$f"
    done
    exit 1
fi
# sweep_registry fires on EXIT, with code 0 if we get here.
