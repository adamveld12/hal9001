#!/usr/bin/env bash
# Publish one update-channel manifest to the gh-pages branch, which GitHub Pages serves as the app's update feed
# (https://adamveld12.github.io/hal9001/<channel>.json). CI only; see docs/releases.md.
#
# usage: scripts/publish-feed.sh <beta.json|stable.json> <commit message>
#
# Only the named manifest is written; the other channel's file on the branch is left alone. The branch is created
# as an orphan (with .nojekyll and a small index.html) if it does not exist yet. A rejected push (the other
# workflow pushed first) is retried from a fresh fetch, which is safe because the two workflows write different files.
#
# env: GH_TOKEN and GITHUB_REPOSITORY (set in Actions) choose the remote and authenticate the push.
#      FEED_REMOTE overrides the remote URL (tests); FEED_BRANCH overrides the branch (default gh-pages).
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <beta.json|stable.json> <commit message>" >&2
  exit 2
fi
manifest=$1
message=$2
name=$(basename "$manifest")
case "$name" in
  beta.json | stable.json) ;;
  *)
    echo "::error::publish-feed: refusing to publish '$name' (only beta.json or stable.json)"
    exit 2
    ;;
esac
if [ ! -s "$manifest" ]; then
  echo "::error::publish-feed: $manifest is missing or empty"
  exit 2
fi
manifest=$(cd "$(dirname "$manifest")" && pwd)/$name

branch=${FEED_BRANCH:-gh-pages}
remote=${FEED_REMOTE:-}
if [ -z "$remote" ]; then
  : "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"
  : "${GH_TOKEN:?GH_TOKEN is not set}"
  remote="https://x-access-token:${GH_TOKEN}@github.com/${GITHUB_REPOSITORY}.git"
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"
git init -q .
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git remote add origin "$remote"

attempts=5
attempt=1
while :; do
  rc=0
  git ls-remote --exit-code --heads origin "$branch" >/dev/null || rc=$?
  if [ "$rc" -eq 0 ]; then
    git fetch -q origin "$branch"
    git checkout -q -B "$branch" FETCH_HEAD
  elif [ "$rc" -eq 2 ]; then
    echo "publish-feed: branch $branch does not exist; creating it"
    git checkout -q --orphan "$branch"
  else
    echo "::error::publish-feed: cannot reach the remote (git ls-remote exit $rc)"
    exit 1
  fi

  cp "$manifest" "$name"
  [ -e .nojekyll ] || : >.nojekyll
  if [ ! -e index.html ]; then
    cat >index.html <<'EOF'
<!doctype html>
<meta charset="utf-8">
<title>HAL9001 update feed</title>
<p>This branch is managed by CI and holds the HAL9001.app update manifests (<code>beta.json</code>, <code>stable.json</code>). Do not edit it by hand.</p>
EOF
  fi
  git add -A
  if git diff --cached --quiet; then
    echo "publish-feed: $name is already up to date on $branch"
    exit 0
  fi
  git commit -q -m "$message"

  if git push -q origin "$branch"; then
    echo "publish-feed: pushed $name to $branch ($message)"
    exit 0
  fi
  if [ "$attempt" -ge "$attempts" ]; then
    echo "::error::publish-feed: push to $branch was rejected $attempts times"
    exit 1
  fi
  echo "publish-feed: push rejected (attempt $attempt of $attempts); refetching"
  attempt=$((attempt + 1))
  sleep $((attempt * 2))
done
