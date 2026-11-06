#!/usr/bin/env bash
# Runs the e2e suite in E2E_SHARDS (default 4) parallel processes of one compiled test binary.
# Every top-level test runs in exactly one shard. RACE=1 adds -race. Extra args go to the test binary.
# Portable to macOS (no GNU-only flags). On failure, prints the log of each failing shard.
set -euo pipefail

shards=${E2E_SHARDS:-4}
case $shards in '' | *[!0-9]* | 0) echo "E2E_SHARDS must be a positive integer, got '$shards'" >&2; exit 2 ;; esac

root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/hal9001-e2e.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

cd "$root"
race=()
[ "${RACE:-}" = 1 ] && race=(-race)
go test -c "${race[@]+"${race[@]}"}" -o "$tmp/e2e.test" ./e2e/

# The test binary runs in the package directory, as `go test` does.
cd "$root/e2e"
"$tmp/e2e.test" -test.list '^Test' | grep '^Test' >"$tmp/all.txt"
total=$(wc -l <"$tmp/all.txt" | tr -d ' ')
[ "$total" -gt 0 ] || { echo "no e2e tests found" >&2; exit 1; }

pids=()
ran=()
for ((i = 0; i < shards; i++)); do
  # Names are Go identifiers, so they need no regexp quoting; anchor each exactly.
  awk -v n="$shards" -v i="$i" '(NR - 1) % n == i' "$tmp/all.txt" >"$tmp/$i.txt"
  [ -s "$tmp/$i.txt" ] || continue
  re="^($(paste -sd '|' - <"$tmp/$i.txt"))\$"
  "$tmp/e2e.test" -test.count=1 -test.v -test.run "$re" "$@" >"$tmp/$i.log" 2>&1 &
  pids+=($!)
  ran+=("$i")
done

fail=0
for idx in "${!pids[@]}"; do
  if ! wait "${pids[$idx]}"; then
    fail=1
    echo "=== e2e shard ${ran[$idx]} FAILED ===" >&2
    cat "$tmp/${ran[$idx]}.log" >&2
  fi
done
[ "$fail" = 0 ] || exit 1

passed=$(cat "$tmp"/*.log | grep -c '^--- PASS: Test' || true)
skipped=$(cat "$tmp"/*.log | grep -c '^--- SKIP: Test' || true)
echo "e2e ok: $((passed + skipped))/$total tests ($skipped skipped) in ${#ran[@]} shards"
[ $((passed + skipped)) = "$total" ] || { echo "e2e: ran $((passed + skipped)) of $total tests" >&2; exit 1; }
