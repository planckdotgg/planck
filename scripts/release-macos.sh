#!/usr/bin/env bash
# Build the macOS release files in dist/: planck-<version>-macos-<arch>.dmg
# and its .sha256.
#
#   scripts/release-macos.sh [--arch arm64|amd64] [--skip-sign] [--skip-notarize]
#
# --skip-sign      sign ad hoc, even when PLANCK_SIGN_IDENTITY is set. The
#                  DMG is not signed and nothing goes to Apple.
# --skip-notarize  sign, but send nothing to Apple.
#
# With a Developer ID identity (PLANCK_SIGN_IDENTITY) the app and the DMG
# are notarized and stapled unless --skip-notarize is given; the
# credentials must then be set, from one of:
#   APPLE_API_KEY_PATH, APPLE_API_KEY_ID, APPLE_API_ISSUER_ID
#       an App Store Connect API key (the release workflow uses this)
#   PLANCK_NOTARY_PROFILE
#       a keychain profile from `xcrun notarytool store-credentials`
set -euo pipefail
cd "$(dirname "$0")/.."

arch=$(uname -m)
[[ "$arch" == x86_64 ]] && arch=amd64
sign=1
notarize=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --arch) arch=${2:?--arch needs arm64 or amd64}; shift 2 ;;
    --skip-sign) sign=0; shift ;;
    --skip-notarize) notarize=0; shift ;;
    *) echo "usage: $0 [--arch arm64|amd64] [--skip-sign] [--skip-notarize]" >&2; exit 2 ;;
  esac
done

[[ $sign -eq 0 ]] && export PLANCK_SIGN_IDENTITY=-
identity="${PLANCK_SIGN_IDENTITY:--}"
if [[ "$identity" == "-" ]]; then
  [[ $notarize -eq 1 ]] && echo "warning: no Developer ID identity, so nothing is notarized. Gatekeeper will refuse this DMG on other Macs." >&2
  notarize=0
elif [[ $notarize -eq 1 && -z "${APPLE_API_KEY_PATH:-}" && -z "${PLANCK_NOTARY_PROFILE:-}" ]]; then
  echo "error: notarization needs APPLE_API_KEY_PATH, APPLE_API_KEY_ID and APPLE_API_ISSUER_ID, or PLANCK_NOTARY_PROFILE. Pass --skip-notarize to build without it." >&2
  exit 1
fi

export PLANCK_DIST_DIR=dist
scripts/bundle-macos.sh --arch "$arch"
app="dist/macos-$arch/Planck.app"
version=$(/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" "$app/Contents/Info.plist")
dmg="dist/planck-$version-macos-$arch.dmg"

notarize_file() {
  local file=$1 auth result status
  if [[ -n "${APPLE_API_KEY_PATH:-}" ]]; then
    auth=(--key "$APPLE_API_KEY_PATH" --key-id "${APPLE_API_KEY_ID:?}" --issuer "${APPLE_API_ISSUER_ID:?}")
  else
    auth=(--keychain-profile "$PLANCK_NOTARY_PROFILE")
  fi
  result=$(mktemp)
  # notarytool can exit 0 for a rejected upload, so the status comes from the result.
  xcrun notarytool submit "$file" "${auth[@]}" --wait --output-format json > "$result" || true
  status=$(plutil -extract status raw -o - "$result" 2>/dev/null || true)
  if [[ "$status" != "Accepted" ]]; then
    echo "error: notarization status of $file is '${status:-unknown}'." >&2
    cat "$result" >&2
    rm -f "$result"
    exit 1
  fi
  rm -f "$result"
}

if [[ $notarize -eq 1 ]]; then
  echo "==> notarize the app (uploads it to Apple)"
  zip="dist/planck-notarize-$arch.zip"
  rm -f "$zip"
  ditto -c -k --keepParent "$app" "$zip"
  notarize_file "$zip"
  rm -f "$zip"
  xcrun stapler staple "$app"
fi

echo "==> $dmg"
rm -f "$dmg"
made=0
if command -v create-dmg >/dev/null; then
  if create-dmg \
    --volname "Planck" \
    --format ULMO \
    --volicon "$app/Contents/Resources/AppIcon.icns" \
    --window-pos 200 120 \
    --window-size 600 400 \
    --icon-size 100 \
    --icon "Planck.app" 150 190 \
    --hide-extension "Planck.app" \
    --app-drop-link 450 190 \
    --no-internet-enable \
    "$dmg" "$app" >/dev/null; then
    made=1
  else
    echo "warning: create-dmg failed; building a plain DMG with hdiutil" >&2
    rm -f "$dmg" dist/rw.*.dmg
  fi
fi
if [[ $made -eq 0 ]]; then
  stage=$(mktemp -d)
  trap 'rm -rf "$stage"' EXIT
  ditto "$app" "$stage/Planck.app"
  ln -s /Applications "$stage/Applications"
  # One step: macOS 27 fails "hdiutil convert" with "Resource temporarily unavailable".
  hdiutil create -volname Planck -srcfolder "$stage" -fs APFS -format ULMO -ov "$dmg" >/dev/null
fi

if [[ "$identity" != "-" ]]; then
  codesign --force --timestamp -s "$identity" "$dmg"
fi
if [[ $notarize -eq 1 ]]; then
  echo "==> notarize the DMG (uploads it to Apple)"
  notarize_file "$dmg"
  xcrun stapler staple "$dmg"
  xcrun stapler validate "$dmg"
  spctl --assess --type open --context context:primary-signature -v "$dmg"
fi

(cd dist && shasum -a 256 "$(basename "$dmg")" > "$(basename "$dmg").sha256")
echo "==> release files:"
/bin/ls -lh "$dmg" "$dmg.sha256"
