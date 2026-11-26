#!/usr/bin/env bash
# Print Planck's version, from Version in internal/version/version.go.
#
#   scripts/version.sh           prints 0.1.0
#   scripts/version.sh v0.1.0    also fails unless the tag is v<version>
set -euo pipefail
cd "$(dirname "$0")/.."
version=$(sed -n 's/^var Version = "\(.*\)"$/\1/p' internal/version/version.go)
[[ -n "$version" ]] || { echo "error: no version in internal/version/version.go" >&2; exit 1; }
if [[ $# -gt 0 ]]; then
  if [[ "$1" != "v$version" ]]; then
    echo "error: the tag is $1, but internal/version/version.go has $version. The tag must be v$version." >&2
    exit 1
  fi
fi
echo "$version"
