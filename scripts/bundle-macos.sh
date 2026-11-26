#!/usr/bin/env bash
# Build and assemble Planck.app: planck-client (the app) and planck-host
# (the command-line host) in Contents/MacOS, the icon in Contents/Resources.
#
#   scripts/bundle-macos.sh [--arch arm64|amd64]
#
# The default arch is this Mac's. The bundle goes to
# ${PLANCK_DIST_DIR:-dist}/macos-<arch>/Planck.app.
#
# Signing: PLANCK_SIGN_IDENTITY names a codesigning identity, such as
# "Developer ID Application: Name (TEAMID)". Unset or "-", the bundle is
# signed ad hoc: it runs on this Mac but Gatekeeper refuses it elsewhere.
set -euo pipefail
cd "$(dirname "$0")/.."
export CGO_ENABLED=0

arch=$(uname -m)
[[ "$arch" == x86_64 ]] && arch=amd64
while [[ $# -gt 0 ]]; do
  case "$1" in
    --arch) arch=${2:?--arch needs arm64 or amd64}; shift 2 ;;
    *) echo "usage: $0 [--arch arm64|amd64]" >&2; exit 2 ;;
  esac
done
[[ "$arch" == arm64 || "$arch" == amd64 ]] || { echo "error: --arch must be arm64 or amd64, not $arch" >&2; exit 2; }

version=$(scripts/version.sh)
commit=$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
build=$(date -u +%Y%m%d%H%M)
identity="${PLANCK_SIGN_IDENTITY:--}"
out="${PLANCK_DIST_DIR:-dist}/macos-$arch"
app="$out/Planck.app"
bin="$out/bin"
ent=packaging/macos/Planck.entitlements
pkg=github.com/planckdotgg/planck/internal/version
ldflags="-s -w -X $pkg.Commit=$commit -X $pkg.BuildDate=$build_date"

echo "==> planck-client, planck-host ($version, darwin/$arch)"
mkdir -p "$bin"
GOOS=darwin GOARCH=$arch go build -trimpath -tags mygo_noinspector -ldflags="$ldflags" -o "$bin/planck-client" ./cmd/planck-client
GOOS=darwin GOARCH=$arch go build -trimpath -ldflags="$ldflags" -o "$bin/planck-host" ./cmd/planck-host

echo "==> $app"
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$bin/planck-client" "$bin/planck-host" "$app/Contents/MacOS/"
cp assets/AppIcon.icns "$app/Contents/Resources/AppIcon.icns"
cp LICENSE THIRD_PARTY_NOTICES.md "$app/Contents/Resources/"
sed -e "s/__VERSION__/$version/" -e "s/__BUILD__/$build/" packaging/macos/Info.plist > "$app/Contents/Info.plist"
plutil -lint "$app/Contents/Info.plist" >/dev/null
printf 'APPL????' > "$app/Contents/PkgInfo"
rm -rf "$bin"

sign=(codesign --force --options runtime -s "$identity")
if [[ "$identity" == "-" ]]; then
  echo "warning: signing ad hoc. Set PLANCK_SIGN_IDENTITY to a Developer ID Application identity for a build other Macs accept." >&2
else
  sign+=(--timestamp)
fi
echo "==> sign ($identity)"
"${sign[@]}" --identifier gg.planck.app.host "$app/Contents/MacOS/planck-host"
"${sign[@]}" --entitlements "$ent" --identifier gg.planck.app "$app"
codesign --verify --deep --strict "$app"
echo "==> signature ok: $app"
