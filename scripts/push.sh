#!/usr/bin/env bash
# Pushes the current branch to origin with gh credentials (never the user's keychain helper).
# - HTTPS rejected for workflow scope while pushing .github/workflows changes: retries over SSH.
# - Pre-push failing only in known flaky tests (scripts/flaky-classify.sh): retries once.
# - Never forces unless --force-with-lease is passed explicitly.
set -euo pipefail

extra=()
for arg in "$@"; do
  case $arg in
    --force-with-lease | --force-with-lease=*) extra+=("$arg") ;;
    *) echo "unsupported argument '$arg' (only --force-with-lease is accepted)" >&2; exit 2 ;;
  esac
done

gh_bin=$(mise which gh 2>/dev/null || command -v gh || true)
if [ -z "$gh_bin" ]; then
  echo "gh not found" >&2
  exit 1
fi

classify="$(cd "$(dirname "$0")" && pwd)/flaky-classify.sh"
ssh_url=git@github.com:adamveld12/hal9001.git
log=$(mktemp "${TMPDIR:-/tmp}/hal9001-push.XXXXXX")
trap 'rm -f "$log"' EXIT

push_https() {
  git -c credential.helper= -c "credential.helper=!$gh_bin auth git-credential" push -u "${extra[@]+"${extra[@]}"}" origin HEAD 2>&1 | tee "$log"
}

touches_workflows() {
  git fetch -q origin main 2>/dev/null || true
  [ -n "$(git diff --name-only origin/main...HEAD -- .github/workflows/)" ]
}

retried=0
while :; do
  if push_https; then exit 0; fi
  if grep -qi 'workflow' "$log" && grep -qiE 'scope|refusing to allow' "$log" && touches_workflows; then
    echo "HTTPS push lacks workflow scope; retrying over SSH" >&2
    exec git push -u "${extra[@]+"${extra[@]}"}" "$ssh_url" HEAD
  fi
  if [ "$retried" = 0 ] && "$classify" <"$log"; then
    retried=1
    echo "pre-push failed in a known flaky test; retrying once" >&2
    continue
  fi
  exit 1
done
