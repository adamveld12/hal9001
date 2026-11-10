#!/usr/bin/env bash
# Mounts the newest bin/HAL9001-*.dmg read-only and checks its contents, then detaches.
set -euo pipefail
cd "$(dirname "$0")/.."
dmg=${1:-$(ls -t bin/HAL9001-*.dmg 2>/dev/null | head -1)}
[ -n "$dmg" ] && [ -f "$dmg" ] || { echo "no DMG found: run \`mise run dmg\` first" >&2; exit 1; }
mnt=$(mktemp -d)
hdiutil attach -nobrowse -readonly -quiet -mountpoint "$mnt" "$dmg"
trap 'hdiutil detach "$mnt" -quiet -force; rmdir "$mnt" 2>/dev/null || true' EXIT
fail=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1" >&2; fail=1; fi; }
check "HAL9001.app present" "[ -d '$mnt/HAL9001.app/Contents/MacOS' ]"
check "Applications symlink to /Applications" "[ \"\$(readlink '$mnt/Applications')\" = /Applications ]"
check ".background/background.png" "[ -s '$mnt/.background/background.png' ]"
check ".background/background@2x.png" "[ -s '$mnt/.background/background@2x.png' ]"
exit $fail
