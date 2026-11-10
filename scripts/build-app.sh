#!/usr/bin/env bash
# Builds the macOS app bundles from bin/hal9001 (run `mise run build` first; `mise run app` does):
#   bin/HAL9001.app        release identity dev.hal9001
#   bin/HAL9001 Dev.app    dev identity dev.hal9001.dev: its own launchd label, notification permission, login item
#                          and CLI name, so testing never touches the release app's state. It is NOT installed by
#                          default; set HAL9001_APP_INSTALL_DIR=~/Applications to install it there (macOS delivers
#                          notifications only to apps in /Applications or ~/Applications).
# Both are signed ad hoc (nested binary first, then the bundle); never use a self-signed identity.
#
# Environment:
#   HAL9001_APP_INSTALL_DIR  opt-in: directory to install the dev copy into (default and "none": no install)
#   HAL9001_RELEASE_VERSION  CI: dotted version (e.g. 0.1.7) used for CFBundleShortVersionString and the full version
#   HAL9001_RELEASE_BUILD    CI: CFBundleVersion (default: 0); must increase with every release
#   HAL9001_APP_DEV_HOME     dev build only: daemon home its bundled service uses (default ~/.hal9001)
#   HAL9001_APP_DEV_PORT     dev build only: daemon port its bundled service uses (default 9001)
set -euo pipefail

cd "$(dirname "$0")/.."
[ -x bin/hal9001 ] || { echo "bin/hal9001 is missing: run \`mise run build\` first (or \`mise run app\`)" >&2; exit 1; }

# The SMAppService API needs macOS 13.
export CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=13.0
export CGO_CFLAGS="-mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0"

version=$(git describe --always --dirty)
short=$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || true)
[[ $short =~ ^[0-9]+(\.[0-9]+){0,2}$ ]] || short=0.0.0
# CFBundleVersion is the CI run number, or 0 for local builds so a local install never outranks a CI release for a
# future updater. (Rebuilding at the same version may need the service re-registered if launchd refuses to start it.)
build=${HAL9001_RELEASE_BUILD:-0}
if [ -n "${HAL9001_RELEASE_VERSION:-}" ]; then
  version=$HAL9001_RELEASE_VERSION
  short=$HAL9001_RELEASE_VERSION
fi

stage=bin/app-build
rm -rf "$stage"
mkdir -p "$stage"
go build -ldflags "-X main.version=$version" -o "$stage/HAL9001" ./cmd/hal9001-app
go run ./cmd/hal9001-app-pack icon -o "$stage/AppIcon.icns"

# assemble <app dir> <bundle id> <name> [hal9001-app-pack bundle flags...]
assemble() {
  local app=$1 id=$2 name=$3
  shift 3
  rm -rf "$app"
  go run ./cmd/hal9001-app-pack bundle -app "$app" -bundle-id "$id" -name "$name" -version "$short" -build "$build" -full-version "$version" "$@"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  cp "$stage/HAL9001" "$app/Contents/MacOS/HAL9001"
  cp bin/hal9001 "$app/Contents/Resources/hal9001"
  cp "$stage/AppIcon.icns" "$app/Contents/Resources/AppIcon.icns"
  codesign --force -s - -i "$id.hal9001" "$app/Contents/Resources/hal9001"
  codesign --force --deep -s - "$app"
  codesign --verify --deep --strict "$app"
  plutil -lint "$app/Contents/Info.plist" "$app"/Contents/Library/LaunchAgents/*.plist >/dev/null
  echo "built $app ($id)"
}

assemble bin/HAL9001.app dev.hal9001 HAL9001

dev_flags=()
[ -z "${HAL9001_APP_DEV_HOME:-}" ] || dev_flags+=(-dev-home "$HAL9001_APP_DEV_HOME")
[ -z "${HAL9001_APP_DEV_PORT:-}" ] || dev_flags+=(-dev-port "$HAL9001_APP_DEV_PORT")
assemble "bin/HAL9001 Dev.app" dev.hal9001.dev "HAL9001 Dev" "${dev_flags[@]+"${dev_flags[@]}"}"

dest=${HAL9001_APP_INSTALL_DIR-none}
if [ -n "$dest" ] && [ "$dest" != none ]; then
  mkdir -p "$dest"
  rm -rf "$dest/HAL9001 Dev.app"
  ditto "bin/HAL9001 Dev.app" "$dest/HAL9001 Dev.app"
  echo "installed $dest/HAL9001 Dev.app"
fi
