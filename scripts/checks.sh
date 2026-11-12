#!/usr/bin/env bash
# Every pre-commit check over the whole tree, in non-writing mode. Runs all steps and reports failures at the end.
set -uo pipefail

cd "$(cd "$(dirname "$0")/.." && pwd)"
failed=()

step() {
  local name=$1
  shift
  echo "==> $name"
  if ! "$@"; then failed+=("$name"); fi
}

gen_api_diff() {
  local tmp
  tmp=$(mktemp "${TMPDIR:-/tmp}/schema.XXXXXX") || return 1
  cp web/src/api/schema.d.ts "$tmp"
  mise exec -- pnpm --dir web gen:api || { cp "$tmp" web/src/api/schema.d.ts; rm -f "$tmp"; return 1; }
  if cmp -s "$tmp" web/src/api/schema.d.ts; then
    rm -f "$tmp"
    return 0
  fi
  diff -u "$tmp" web/src/api/schema.d.ts | head -50
  cp "$tmp" web/src/api/schema.d.ts
  rm -f "$tmp"
  echo "web/src/api/schema.d.ts is stale; run: mise exec -- pnpm --dir web gen:api" >&2
  return 1
}

gitleaks_range() {
  git fetch -q origin main 2>/dev/null || true
  mise exec -- gitleaks git --redact --no-banner --log-opts="origin/main..HEAD"
}

step "skill frontmatter" mise exec -- go run ./scripts/skillcheck
step "golangci-lint fmt --diff" mise exec -- golangci-lint fmt --diff ./...
step "prettier --check" mise exec -- pnpm --dir web format:check
step "gen:api diff" gen_api_diff
step "gitleaks origin/main..HEAD" gitleaks_range
step "go mod tidy -diff" mise exec -- go mod tidy -diff
step "golangci-lint run" mise exec -- golangci-lint run ./...
step "go test -short" mise exec -- go test -short ./...
step "web lint" mise exec -- pnpm --dir web lint
step "web typecheck" mise exec -- pnpm --dir web typecheck
step "web test" mise exec -- pnpm --dir web test

if [ ${#failed[@]} -gt 0 ]; then
  printf 'FAILED: %s\n' "${failed[@]}" >&2
  exit 1
fi
echo "all checks passed"
