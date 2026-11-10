#!/usr/bin/env bash
# Builds bin/HAL9001-<version>.dmg from bin/HAL9001.app (run `mise run app` first; `mise run release` does both).
# Uses only macOS tools: hdiutil, osascript (Finder layout), SetFile (optional, volume icon flag).
# The Finder layout step needs a GUI session with automation permission for Finder; without it the DMG is still
# produced, just without the custom window, and a warning is printed.
set -euo pipefail

cd "$(dirname "$0")/.."
app=bin/HAL9001.app
[ -d "$app" ] || { echo "$app is missing: run \`mise run app\` first" >&2; exit 1; }

version=$(git describe --always --dirty | sed 's/^v//')
vol=HAL9001
out=bin/HAL9001-$version.dmg
rw=bin/dmg-build/rw.dmg
stage=bin/dmg-build/stage
mount=bin/dmg-build/mnt

# A leftover mount from an interrupted run would make hdiutil pick a different volume name.
[ ! -d "/Volumes/$vol" ] || hdiutil detach "/Volumes/$vol" -force >/dev/null 2>&1 || true
rm -rf bin/dmg-build "$out"
mkdir -p "$stage/.background"

ditto "$app" "$stage/HAL9001.app"
ln -s /Applications "$stage/Applications"
go run ./cmd/hal9001-app-pack dmg-background -dir "$stage/.background"
cp "$app/Contents/Resources/AppIcon.icns" "$stage/.VolumeIcon.icns"

size_mb=$(( $(du -sm "$stage" | cut -f1) + 20 ))
hdiutil create -quiet -srcfolder "$stage" -volname "$vol" -fs HFS+ -format UDRW -size "${size_mb}m" "$rw"
mkdir -p "$mount"
hdiutil attach -quiet -readwrite -noverify -noautoopen -mountpoint "$mount" "$rw"
detach() { hdiutil detach "$mount" -quiet 2>/dev/null || hdiutil detach "$mount" -force -quiet; }
trap 'detach 2>/dev/null || true' EXIT

# Finder addresses the volume by name, so the layout script needs it mounted under /Volumes; re-attach there.
detach
hdiutil attach -quiet -readwrite -noverify -noautoopen "$rw"
mount=/Volumes/$vol

if command -v SetFile >/dev/null; then
  SetFile -a C "$mount" || echo "warning: SetFile -a C failed; the volume will use the default icon" >&2
  SetFile -a V "$mount/.background" "$mount/.VolumeIcon.icns" 2>/dev/null || true
else
  echo "warning: SetFile not found (Xcode command line tools); the volume icon will not be applied" >&2
fi

# Window 640x400 at (200,120); icon centres match internal/menubar/dmgbg.go.
layout=$(cat <<OSA
with timeout of 30 seconds
tell application "Finder"
  tell disk "$vol"
    open
    set current view of container window to icon view
    set toolbar visible of container window to false
    set statusbar visible of container window to false
    set the bounds of container window to {200, 120, 840, 520}
    set opts to the icon view options of container window
    set arrangement of opts to not arranged
    set icon size of opts to 128
    set text size of opts to 13
    set background picture of opts to file ".background:background.png"
    set position of item "HAL9001.app" of container window to {170, 190}
    set position of item "Applications" of container window to {470, 190}
    close
    open
    update without registering applications
    delay 2
    close
  end tell
end tell
end timeout
OSA
)
if timeout_out=$(osascript -e "$layout" 2>&1); then
  sync; sleep 2 # let Finder write .DS_Store
else
  echo "warning: Finder layout step failed (no GUI session or Finder automation permission?): $timeout_out" >&2
  echo "warning: producing the DMG without the custom window layout" >&2
fi

sync
detach
trap - EXIT
hdiutil convert -quiet "$rw" -format UDZO -imagekey zlib-level=9 -o "$out"
rm -rf bin/dmg-build
echo "built $out ($(du -h "$out" | cut -f1))"
