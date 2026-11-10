#!/usr/bin/env bash
# Builds bin/HAL9001-<version>.dmg from bin/HAL9001.app (run `mise run app` first; `mise run release` does both).
# Layout (background, icon positions, window) is written directly into .DS_Store by dmgbuild (pinned via uvx), so no
# Finder session or Automation permission is needed. Any failure aborts the build.
set -euo pipefail

cd "$(dirname "$0")/.."
app=bin/HAL9001.app
[ -d "$app" ] || { echo "$app is missing: run \`mise run app\` first" >&2; exit 1; }

version=$(git describe --always --dirty | sed 's/^v//')
out=bin/HAL9001-$version.dmg
work=bin/dmg-build

rm -rf "$work" "$out"
mkdir -p "$work/bg"
trap 'rm -rf "$work"' EXIT

go run ./cmd/hal9001-app-pack dmg-background -dir "$work/bg"
# Multi-resolution TIFF: Finder picks the @2x representation on Retina displays.
tiffutil -cathidpicheck "$work/bg/background.png" "$work/bg/background@2x.png" -out "$work/background.tiff"

DMG_APP="$app" DMG_BACKGROUND="$work/background.tiff" DMG_ICON="$app/Contents/Resources/AppIcon.icns" \
  uvx --from dmgbuild==1.6.7 dmgbuild -s scripts/dmg-settings.py HAL9001 "$out"
echo "built $out ($(du -h "$out" | cut -f1))"
