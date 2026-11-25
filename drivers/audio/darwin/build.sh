#!/bin/sh
# Builds PlanckAudio.driver, runs its tests, and signs it. It never
# installs anything; see README.md for that.
#
#   ./build.sh                      ad-hoc signed, for this Mac only
#   PLANCK_SIGN_IDENTITY="Developer ID Application: Name (TEAMID)" ./build.sh
#   PLANCK_SIGN_IDENTITY=... PLANCK_NOTARY_PROFILE=planck ./build.sh
#
# PLANCK_AUDIO_BUILD_DIR sets the build directory (default ./build).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
build=${PLANCK_AUDIO_BUILD_DIR:-"$here/build"}
identity=${PLANCK_SIGN_IDENTITY:--}
profile=${PLANCK_NOTARY_PROFILE:-}

cmake -S "$here" -B "$build" -DCMAKE_BUILD_TYPE=Release
cmake --build "$build" --config Release
ctest --test-dir "$build" --output-on-failure

bundle="$build/PlanckAudio.driver"
if [ "$identity" = "-" ]; then
	codesign --force --sign - "$bundle"
else
	codesign --force --sign "$identity" --options runtime --timestamp "$bundle"
fi
codesign --verify --strict --verbose=2 "$bundle"

if [ -n "$profile" ]; then
	if [ "$identity" = "-" ]; then
		echo "notarizing needs PLANCK_SIGN_IDENTITY set to a Developer ID Application identity" >&2
		exit 1
	fi
	zip="$build/PlanckAudio.driver.zip"
	rm -f "$zip"
	ditto -c -k --keepParent "$bundle" "$zip"
	xcrun notarytool submit "$zip" --keychain-profile "$profile" --wait
	xcrun stapler staple "$bundle"
	xcrun stapler validate "$bundle"
fi

echo "built $bundle"
