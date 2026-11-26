#!/usr/bin/env bash
# Build Planck.app from this checkout and install it, for local development.
#
#   scripts/install-macos.sh [--arch arm64|amd64] [--dest PATH] [--identity ID]
#                            [--adhoc] [--launch | --no-launch]
#
# The app is built and signed by scripts/bundle-macos.sh into a temporary
# directory, verified, and only then swapped into --dest (default
# /Applications/Planck.app). A running copy inside --dest is stopped first.
# --dest /Applications/Planck.app launches the app afterwards; --no-launch
# never does.
#
# Signing identity, first match wins:
#   --adhoc                              ad hoc signature
#   --identity ID                        a name or hash from `security find-identity`
#   PLANCK_SIGN_IDENTITY                 the same, from the environment
#   PLANCK_SIGN_IDENTITY in .env.local   the same, from a git-ignored file in the repo root
# With none of these, a single "Developer ID Application" identity in the
# keychain is used. Several is an error, and none means an ad hoc signature.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

default_dest=/Applications/Planck.app
arch=$(uname -m)
[[ "$arch" == x86_64 ]] && arch=amd64
dest=$default_dest
identity=""
adhoc=0
launch=""
no_launch=0

usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }
die() { echo "error: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --arch) arch=${2:?--arch needs arm64 or amd64}; shift 2 ;;
    --dest) dest=${2:?--dest needs a path}; shift 2 ;;
    --identity) identity=${2:?--identity needs a value}; shift 2 ;;
    --adhoc) adhoc=1; shift ;;
    --launch) launch=1; shift ;;
    --no-launch) no_launch=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done
[[ "$arch" == arm64 || "$arch" == amd64 ]] || { echo "error: --arch must be arm64 or amd64, not $arch" >&2; exit 2; }
[[ "$(basename "$dest")" == *.app ]] || die "--dest must end in .app: $dest"

dest_dir=$(dirname "$dest")
[[ -d "$dest_dir" ]] && dest_dir=$(cd "$dest_dir" && pwd -P)
dest="$dest_dir/$(basename "$dest")"
if [[ -z "$launch" ]]; then
  [[ "$dest" == "$default_dest" ]] && launch=1 || launch=0
fi
[[ "$no_launch" == 1 ]] && launch=0

# Reads PLANCK_SIGN_IDENTITY from .env.local as data; the file is never sourced.
env_local_identity() {
  [[ -f .env.local ]] || return 0
  local line value=""
  while IFS= read -r line || [[ -n "$line" ]]; do
    line=${line%$'\r'}
    if [[ "$line" =~ ^[[:space:]]*(export[[:space:]]+)?PLANCK_SIGN_IDENTITY[[:space:]]*=[[:space:]]*(.*)$ ]]; then
      value=${BASH_REMATCH[2]}
      value=${value%"${value##*[![:space:]]}"}
      if [[ "$value" =~ ^\"(.*)\"$ || "$value" =~ ^\'(.*)\'$ ]]; then value=${BASH_REMATCH[1]}; fi
    fi
  done < .env.local
  printf '%s' "$value"
}

if [[ "$adhoc" == 1 ]]; then
  identity="-"
  echo "==> signing ad hoc (--adhoc)"
elif [[ -n "$identity" ]]; then
  echo "==> signing identity from --identity: $identity"
elif [[ -n "${PLANCK_SIGN_IDENTITY:-}" ]]; then
  identity=$PLANCK_SIGN_IDENTITY
  echo "==> signing identity from PLANCK_SIGN_IDENTITY: $identity"
elif [[ -n "$(env_local_identity)" ]]; then
  identity=$(env_local_identity)
  echo "==> signing identity from .env.local: $identity"
else
  found=()
  while IFS= read -r name; do
    [[ -n "$name" ]] && found+=("$name")
  done < <(security find-identity -v -p codesigning 2>/dev/null | sed -n 's/^[^"]*"\(Developer ID Application:[^"]*\)".*/\1/p' | sort -u)
  if [[ ${#found[@]} -eq 1 ]]; then
    identity=${found[0]}
    echo "==> signing with the only Developer ID Application identity: $identity"
  elif [[ ${#found[@]} -gt 1 ]]; then
    echo "error: ${#found[@]} Developer ID Application identities found, and none was chosen:" >&2
    printf '  %s\n' "${found[@]}" >&2
    echo "Set PLANCK_SIGN_IDENTITY (in the environment or in .env.local), pass --identity \"...\", or pass --adhoc." >&2
    exit 1
  else
    identity="-"
    echo "warning: no Developer ID Application identity found; signing ad hoc." >&2
  fi
fi

stage=$(mktemp -d "${TMPDIR:-/tmp}/planck-install.XXXXXX")
new="$dest_dir/.Planck.app.new.$$"
old="$dest_dir/.Planck.app.old.$$"
cleanup() { rm -rf "$stage" "$new"; }
trap cleanup EXIT

PLANCK_SIGN_IDENTITY="$identity" PLANCK_DIST_DIR="$stage" scripts/bundle-macos.sh --arch "$arch"
built="$stage/macos-$arch/Planck.app"
echo "==> verify staged bundle"
codesign --verify --deep --strict "$built"

# Only processes whose executable is inside --dest are stopped.
running_pids() {
  ps -axo pid=,comm= | awk -v prefix="$dest/Contents/" '{ pid=$1; sub(/^ *[0-9]+ +/, ""); if (index($0, prefix) == 1) print pid }'
}
pids=$(running_pids)
if [[ -n "$pids" ]]; then
  echo "==> stopping running $dest"
  # shellcheck disable=SC2086
  kill $pids 2>/dev/null || true
  for _ in $(seq 1 50); do
    [[ -z "$(running_pids)" ]] && break
    sleep 0.1
  done
  pids=$(running_pids)
  # shellcheck disable=SC2086
  [[ -z "$pids" ]] || kill -9 $pids 2>/dev/null || true
fi

echo "==> install $dest"
mkdir -p "$dest_dir"
dest_dir=$(cd "$dest_dir" && pwd -P)
new="$dest_dir/.Planck.app.new.$$"
old="$dest_dir/.Planck.app.old.$$"
ditto "$built" "$new"
had_old=0
if [[ -e "$dest" ]]; then
  mv "$dest" "$old"
  had_old=1
fi
if ! mv "$new" "$dest"; then
  [[ "$had_old" == 1 ]] && mv "$old" "$dest"
  die "could not move the new app to $dest; the previous app was restored"
fi
rm -rf "$old"

codesign --verify --deep --strict "$dest"
echo "==> installed and verified: $dest ($(scripts/version.sh), darwin/$arch)"
if [[ "$launch" == 1 ]]; then
  echo "==> launching"
  open "$dest"
fi
