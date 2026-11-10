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
check ".background.tiff image" "[ -s '$mnt/.background.tiff' ]"
check ".DS_Store present" "[ -s '$mnt/.DS_Store' ]"
# Layout: parse .DS_Store (ds_store ships as a dmgbuild dependency) for the background and icon centres.
check ".DS_Store sets background and icon positions" "uvx --from dmgbuild==1.6.7 python - '$mnt/.DS_Store' <<'PY'
import sys
from ds_store import DSStore
with DSStore.open(sys.argv[1], 'r') as d:
    bwsp = d['.']['icvp']
    assert bwsp.get('backgroundType') == 2 and bwsp.get('backgroundImageAlias'), 'background picture not set: %r' % bwsp
    assert bwsp.get('iconSize') == 128, 'icon size %r' % bwsp.get('iconSize')
    for name, want in (('HAL9001.app', (170, 190)), ('Applications', (470, 190))):
        got = d[name]['Iloc']
        assert got == want, '%s at %r, want %r' % (name, got, want)
PY"
exit $fail
