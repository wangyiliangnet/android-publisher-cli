#!/bin/sh
set -eu

go_binary=${1:-go}
identifier=io.github.wangyiliangnet.android-publisher-cli.release
mode=${PUBLISHER_PACKAGE_MODE:-local}
identity=
fail() { printf '%s\n' "$1" >&2; exit 1; }
valid_version() {
    case "$1" in ''|*[!0-9A-Za-z.+-]*) return 1 ;; esac
    [ "${#1}" -le 100 ] || return 1
    printf '%s\n' "$1" | LC_ALL=C /usr/bin/grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$' || return 1
    core=${1%%+*}
    case "$core" in
        *-*) printf '%s\n' "${core#*-}" | /usr/bin/awk -F. '{ for (i=1; i<=NF; i++) if ($i ~ /^0[0-9]+$/) exit 1 }' ;;
        *) return 0 ;;
    esac
}
version=${PUBLISHER_RELEASE_VERSION:-0.1.0-dev}
valid_version "$version" || fail 'Invalid release version.'
case "$mode" in
    local) output=bin/release ;;
    ci-preview)
        [ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ] || fail 'CI previews require Apple Silicon macOS.'
        [ "${PUBLISHER_DEVELOPER_ID:-0}" = 0 ] && [ -z "${PUBLISHER_NOTARY_PROFILE:-}" ] || fail 'CI previews cannot use Developer ID or notarization.'
        output=bin/release-ci-preview
        ;;
    *) fail 'Package mode must be local or ci-preview.' ;;
esac

# CI previews deliberately never resolve a configured certificate or signing file.
if [ "$mode" = local ]; then
    identity_file=${PUBLISHER_SIGNING_IDENTITY_FILE:-"${HOME:?}/Library/Application Support/AndroidPublisherCLI/Signing/identity.sha1"}
    if [ -e "$identity_file" ] || [ -L "$identity_file" ]; then
        [ "$(uname -s)" = Darwin ] || fail 'Configured local signing requires macOS.'
        [ ! -L "$identity_file" ] && [ -f "$identity_file" ] || fail 'Signing identity must be a regular local file.'
        [ "$(/usr/bin/stat -f %u "$identity_file")" = "$(id -u)" ] || fail 'Signing identity must belong to the current user.'
        case $(/usr/bin/stat -f %Lp "$identity_file") in
            400|600) ;;
            *) fail 'Signing identity file must have mode 0400 or 0600.' ;;
        esac
        identity=$(LC_ALL=C tr -d '\r\n' < "$identity_file" | tr '[:lower:]' '[:upper:]')
        [ "${#identity}" = 40 ] || fail 'Signing identity must contain a certificate SHA-1 fingerprint.'
        case "$identity" in *[!0-9A-F]*) fail 'Invalid signing identity fingerprint.' ;; esac
        identities=$(/usr/bin/security find-identity -v -p codesigning)
        case "$identities" in
            *" $identity \""*) ;;
            *) fail 'Configured signing identity is unavailable or expired; existing release was preserved.' ;;
        esac
    elif [ "${PUBLISHER_REQUIRE_SIGNING:-0}" = 1 ] || [ "${PUBLISHER_DEVELOPER_ID:-0}" = 1 ]; then
        fail 'Configure the local signing identity before building a signed release.'
    elif [ -f "$output" ] && [ -x /usr/bin/codesign ]; then
        previous=$(/usr/bin/codesign -d --verbose=2 "$output" 2>&1 || true)
        case "$previous" in
            *'Authority='*) fail 'Signing configuration is missing; refusing to replace a certificate-signed release.' ;;
        esac
    fi
fi

mkdir -p bin
build_dir=$(mktemp -d bin/.release-build.XXXXXX)
trap 'rm -rf "$build_dir"' EXIT HUP INT TERM
commit=$(git rev-parse --verify HEAD)
if [ -n "$(git status --porcelain)" ]; then commit="$commit-dirty"; fi
build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
"$go_binary" build -trimpath -ldflags "-X github.com/wangyiliangnet/android-publisher-cli/internal/cli.Version=$version -X github.com/wangyiliangnet/android-publisher-cli/internal/cli.Commit=$commit -X github.com/wangyiliangnet/android-publisher-cli/internal/cli.BuildDate=$build_date" -o "$build_dir/release" ./cmd/release
if [ "$mode" = ci-preview ]; then
    /usr/bin/codesign --force --sign - --identifier "$identifier" --timestamp=none "$build_dir/release"
    /usr/bin/codesign --verify --strict "$build_dir/release"
    signature=$(/usr/bin/codesign -dv --verbose=2 "$build_dir/release" 2>&1)
    printf '%s\n' "$signature" | /usr/bin/grep -qx 'Signature=adhoc' || fail 'CI preview must have an ad-hoc signature.'
    printf '%s\n' 'Built ad-hoc signed CI preview; no stable trusted identity or notarization.'
elif [ -n "$identity" ]; then
    rule="identifier \"$identifier\" and anchor apple generic and certificate leaf = H\"$identity\""
    if [ "${PUBLISHER_DEVELOPER_ID:-0}" = 1 ]; then
        case "$identities" in *" $identity \"Developer ID Application:"*) ;; *) fail 'Release distribution requires Developer ID Application.' ;; esac
        /usr/bin/codesign --force --sign "$identity" --identifier "$identifier" \
            --requirements "=designated => $rule" --options runtime --timestamp "$build_dir/release"
    else
        /usr/bin/codesign --force --sign "$identity" --identifier "$identifier" \
            --requirements "=designated => $rule" --timestamp=none "$build_dir/release"
    fi
    /usr/bin/codesign --verify --strict --test-requirement "=$rule" "$build_dir/release"
    printf '%s\n' 'Verified the configured local signature.'
fi
mv -f "$build_dir/release" "$output"
