#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
for script in tools/build-release.sh tools/package-macos.sh tools/install-macos.sh tools/smoke-package-macos.sh tools/check-package-scripts.sh; do
 /bin/sh -n "$script"
done
workspace=$(mktemp -d "${TMPDIR:-/tmp}/publisher-cli-script-test.XXXXXX")
trap 'rm -rf "$workspace"' EXIT HUP INT TERM
for version in '../escape' '1.0.0;touch-test' '01.0.0' '1.0.0-01' '1.0' '1.0.0/escape' "$(printf '1.0.0\n-Xevil')"; do
 if PUBLISHER_RELEASE_VERSION=$version /bin/sh tools/build-release.sh "$workspace/no-go" > "$workspace/reject.log" 2>&1; then
  printf '%s\n' 'Invalid version was accepted.' >&2; exit 1
 fi
 /usr/bin/grep -q 'Invalid release version.' "$workspace/reject.log"
done
if PUBLISHER_PACKAGE_MODE=unexpected /bin/sh tools/build-release.sh "$workspace/no-go" > "$workspace/reject.log" 2>&1; then
 printf '%s\n' 'Unknown package mode was accepted.' >&2; exit 1
fi
/usr/bin/grep -q 'Package mode must be local or ci-preview.' "$workspace/reject.log"
if [ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ]; then
 # Missing explicit identity must fail before compilation, never fall back to ad-hoc.
 if PUBLISHER_PACKAGE_MODE=local PUBLISHER_REQUIRE_SIGNING=1 PUBLISHER_SIGNING_IDENTITY_FILE="$workspace/missing-identity" /bin/sh tools/build-release.sh "$workspace/no-go" > "$workspace/reject.log" 2>&1; then
  printf '%s\n' 'Required signing silently fell back.' >&2; exit 1
 fi
 /usr/bin/grep -q 'Configure the local signing identity' "$workspace/reject.log"
 if PUBLISHER_PACKAGE_MODE=ci-preview PUBLISHER_NOTARY_PROFILE=forbidden /bin/sh tools/build-release.sh "$workspace/no-go" > "$workspace/reject.log" 2>&1; then
  printf '%s\n' 'CI preview accepted notarization credentials.' >&2; exit 1
 fi
 /usr/bin/grep -q 'CI previews cannot use Developer ID or notarization.' "$workspace/reject.log"
fi
printf '%s\n' 'Package script syntax, version/path validation, explicit signing requirement and CI mode guards passed.'
