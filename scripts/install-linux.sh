#!/usr/bin/env bash
# Install Planck for your user on Linux.
#
#   scripts/install-linux.sh [--prefix DIR] [--systemd] [--arch x86_64|aarch64]
#
# From a source checkout it builds planck-client and planck-host (pure Go,
# CGO_ENABLED=0). From an unpacked release archive (install.sh next to bin/)
# it copies the programs that came in it. It installs:
#
#   <prefix>/bin/planck-client, planck-host      (default prefix ~/.local)
#   <prefix>/share/applications/planck.desktop
#   <prefix>/share/icons/hicolor/*/apps/planck.png
#   ~/.config/systemd/user/planck-host.service   with --systemd
#
# It never uses sudo. The udev rule that lets planck-host create virtual
# input devices is system-wide, so it only prints how to install it.
set -euo pipefail

prefix="${HOME}/.local"
systemd=0
arch=$(uname -m)

usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }
die() { echo "error: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix) prefix=${2:?--prefix needs a directory}; shift 2 ;;
    --systemd) systemd=1; shift ;;
    --arch) arch=${2:?--arch needs x86_64 or aarch64}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done
[[ "$(uname -s)" == Linux ]] || die "this installs for Linux; use install-macos.sh or install-windows.ps1 here"
case "$arch" in
  x86_64|amd64) arch=x86_64; goarch=amd64 ;;
  aarch64|arm64) arch=aarch64; goarch=arm64 ;;
  *) die "unsupported architecture $arch (want x86_64 or aarch64)" ;;
esac
prefix=$(mkdir -p "$prefix" && cd "$prefix" && pwd -P)

here=$(cd "$(dirname "$0")" && pwd)
if [[ -f "$here/bin/planck-client" && -f "$here/bin/planck-host" ]]; then
  mode=archive
  src=$here
  desktop=$src/share/applications/planck.desktop
  icons=$src/share/icons/hicolor
  rules=$src/share/planck/60-planck-uinput.rules
  unit=$src/share/systemd/user/planck-host.service
else
  mode=checkout
  src=$(cd "$here/.." && pwd)
  desktop=$src/packaging/linux/planck.desktop
  icons=""
  rules=$src/packaging/linux/60-planck-uinput.rules
  unit=$src/packaging/linux/planck-host.service
fi

bin=$prefix/bin
mkdir -p "$bin" "$prefix/share/applications"

if [[ "$mode" == checkout ]]; then
  command -v go >/dev/null || die "go is not installed; install Go, or use a release archive"
  echo "==> build planck-client and planck-host (linux/$goarch)"
  (cd "$src" && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go build -trimpath -tags mygo_noinspector -o "$bin/planck-client.new" ./cmd/planck-client \
    && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go build -trimpath -o "$bin/planck-host.new" ./cmd/planck-host)
else
  echo "==> install planck-client and planck-host"
  cp "$src/bin/planck-client" "$bin/planck-client.new"
  cp "$src/bin/planck-host" "$bin/planck-host.new"
fi
# A running copy keeps its old file; the rename leaves it alone.
chmod 0755 "$bin/planck-client.new" "$bin/planck-host.new"
mv -f "$bin/planck-client.new" "$bin/planck-client"
mv -f "$bin/planck-host.new" "$bin/planck-host"

quote_exec() {
  local s=${1//\\/\\\\}
  s=${s//\"/\\\"}
  printf '"%s"' "$s"
}

echo "==> desktop entry"
awk -v val="$(quote_exec "$bin/planck-client")" '{ if ($0 ~ /^Exec=/) print "Exec=" val; else print }' "$desktop" > "$prefix/share/applications/planck.desktop"

echo "==> icons"
if [[ "$mode" == archive ]]; then
  mkdir -p "$prefix/share/icons"
  cp -r "$icons" "$prefix/share/icons/"
else
  for png in "$src"/assets/png/linux-hicolor/planck-*.png; do
    size=${png##*-}
    size=${size%.png}
    mkdir -p "$prefix/share/icons/hicolor/${size}x${size}/apps"
    cp "$png" "$prefix/share/icons/hicolor/${size}x${size}/apps/planck.png"
  done
fi
command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -q -t -f "$prefix/share/icons/hicolor" 2>/dev/null || true
command -v update-desktop-database >/dev/null && update-desktop-database -q "$prefix/share/applications" 2>/dev/null || true

if [[ "$systemd" == 1 ]]; then
  echo "==> systemd user service"
  unitdir=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
  mkdir -p "$unitdir"
  awk -v val="$(quote_exec "$bin/planck-host")" '{ if ($0 ~ /^ExecStart=/) print "ExecStart=" val; else print }' "$unit" > "$unitdir/planck-host.service"
  if command -v systemctl >/dev/null && systemctl --user daemon-reload 2>/dev/null; then
    echo "    enable it with: systemctl --user enable --now planck-host"
  else
    echo "    wrote $unitdir/planck-host.service; systemd is not running for this user, so run daemon-reload later"
  fi
fi

echo "==> installed to $prefix"
case ":$PATH:" in
  *":$bin:"*) ;;
  *) echo "    $bin is not on your PATH; add it to run planck-client from a terminal" ;;
esac

if [[ ! -w /dev/uinput ]]; then
  cat <<MSG

planck-host injects keyboard, mouse and gamepad input through /dev/uinput, and
your user cannot write to it yet. As root:

  install -m 0644 $rules /etc/udev/rules.d/60-planck-uinput.rules
  echo uinput > /etc/modules-load.d/uinput.conf
  modprobe uinput
  udevadm control --reload-rules && udevadm trigger --name-match=uinput

then log in again. Without it the host falls back to X11 XTest (X.Org) or the
RemoteDesktop portal (Wayland), which is less complete. See README.txt.
MSG
fi
