#!/usr/bin/env bash
# Creates a worktree .worktrees/<name> on a new branch from origin/main.
# Usage: scripts/wt.sh <name> [branch]   (branch defaults to feat/<name>)
set -euo pipefail

name=${1:-}
branch=${2:-}
if [ -z "$name" ]; then
  echo "usage: mise run wt <name> [branch]" >&2
  exit 2
fi
case $name in *[!A-Za-z0-9._-]* | -*) echo "invalid worktree name '$name'" >&2; exit 2 ;; esac
branch=${branch:-feat/$name}

# Resolve the main checkout even when run from inside another worktree.
common=$(git rev-parse --path-format=absolute --git-common-dir)
main=$(cd "$common/.." && pwd)
dest="$main/.worktrees/$name"

if [ -e "$dest" ]; then
  echo "refusing: $dest already exists" >&2
  exit 1
fi
mkdir -p "$main/.worktrees"

git -C "$main" fetch origin
git -C "$main" worktree add "$dest" -b "$branch" origin/main
if [ ! -d "$dest/web/node_modules" ]; then
  (cd "$dest" && mise exec -- pnpm --dir web install --frozen-lockfile) >&2
fi
(cd "$dest" && git config merge.keepours.driver true)
echo "$dest"
