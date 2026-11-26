#!/usr/bin/env bash
# Build the Linux release tarballs in dist/:
# planck-<version>-linux-<x86_64|aarch64>.tar.gz, each with its .sha256.
#
#   scripts/bundle-linux.sh [x86_64|aarch64 ...]
#
# With no argument it builds both. The binaries are pure Go
# (CGO_ENABLED=0), so any host can cross-build them.
set -euo pipefail
cd "$(dirname "$0")/.."
export CGO_ENABLED=0

arches=("$@")
[[ ${#arches[@]} -gt 0 ]] || arches=(x86_64 aarch64)
version=$(scripts/version.sh)
commit=$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
pkg=github.com/planckdotgg/planck/internal/version
ldflags="-s -w -X $pkg.Commit=$commit -X $pkg.BuildDate=$build_date"
# A release (PLANCK_OFFICIAL_BUILD=1, set by the Release workflow) sends its
# telemetry as a release, not as test signals. PLANCK_TELEMETRY_APP_ID is the
# TelemetryDeck App ID of the official app (internal/telemetry); without it
# the build sends no telemetry.
if [[ "${PLANCK_OFFICIAL_BUILD:-}" == 1 ]]; then
  ldflags+=" -X $pkg.OfficialBuild=1"
fi
if [[ -n "${PLANCK_TELEMETRY_APP_ID:-}" ]]; then
  ldflags+=" -X github.com/planckdotgg/planck/internal/telemetry.DefaultAppID=$PLANCK_TELEMETRY_APP_ID"
fi
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

mkdir -p dist
for arch in "${arches[@]}"; do
  case "$arch" in
    x86_64) goarch=amd64 ;;
    aarch64) goarch=arm64 ;;
    *) echo "error: unknown arch $arch (want x86_64 or aarch64)" >&2; exit 2 ;;
  esac
  name="planck-$version-linux-$arch"
  root="dist/linux-$arch/$name"
  echo "==> $name"
  rm -rf "dist/linux-$arch"
  mkdir -p "$root/bin" "$root/share/applications" "$root/share/planck" "$root/share/systemd/user"
  GOOS=linux GOARCH=$goarch go build -trimpath -tags mygo_noinspector -ldflags="$ldflags" -o "$root/bin/planck-client" ./cmd/planck-client
  GOOS=linux GOARCH=$goarch go build -trimpath -ldflags="$ldflags" -o "$root/bin/planck-host" ./cmd/planck-host
  cp LICENSE THIRD_PARTY_NOTICES.md "$root/"
  sed -e "s/__VERSION__/$version/" -e "s/__ARCH__/$arch/" packaging/linux/README.txt > "$root/README.txt"
  cp packaging/linux/planck.desktop "$root/share/applications/planck.desktop"
  cp packaging/linux/60-planck-uinput.rules "$root/share/planck/60-planck-uinput.rules"
  cp packaging/linux/planck-host.service "$root/share/systemd/user/planck-host.service"
  cp scripts/install-linux.sh "$root/install.sh"
  for png in assets/png/linux-hicolor/planck-*.png; do
    size=${png##*-}
    size=${size%.png}
    mkdir -p "$root/share/icons/hicolor/${size}x${size}/apps"
    cp "$png" "$root/share/icons/hicolor/${size}x${size}/apps/planck.png"
  done
  tarball="dist/$name.tar.gz"
  COPYFILE_DISABLE=1 tar --no-xattrs -C "dist/linux-$arch" -czf "$tarball" "$name"
  rm -rf "dist/linux-$arch"
  (cd dist && sha256 "$(basename "$tarball")" > "$(basename "$tarball").sha256")
  /bin/ls -lh "$tarball"
done
