#!/bin/sh
set -eu
# Installs only this bundle. Publisher data and Keychain are never copied.
bundle_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
prefix="${HOME:?}/.local/android-publisher-cli"
action=install
rollback_version=
while [ "$#" -gt 0 ]; do
 case "$1" in
  --prefix) [ "$#" -ge 2 ] || exit 2; prefix=$2; shift 2 ;;
  --rollback) [ "$#" -ge 2 ] && [ "$action" = install ] || exit 2; action=rollback; rollback_version=$2; shift 2 ;;
  --uninstall) [ "$action" = install ] || exit 2; action=uninstall; shift ;;
  *) printf '%s\n' 'Usage: sh install.sh [--prefix ABSOLUTE_DIR] [--rollback VERSION | --uninstall]' >&2; exit 2 ;;
 esac
done
fail() { printf '%s\n' "$1" >&2; exit 1; }
[ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ] || fail 'This bundle supports Apple Silicon Macs only.'
case "$prefix" in /*) ;; *) fail 'Installation prefix must be absolute.' ;; esac
case "$prefix" in /|"${HOME:?}"|*/../*|*/..|*/./*|*/.|*//*|*/) fail 'Choose a dedicated installation directory without traversal components.' ;; esac
# Refuse symlinks in the complete installation ancestry.
parent=$prefix
while [ "$parent" != / ]; do [ ! -L "$parent" ] || fail 'Installation paths must not be symlinks.'; parent=$(dirname "$parent"); done
umask 077
mkdir -p "$prefix"
[ "$(/usr/bin/stat -f %u "$prefix")" = "$(id -u)" ] || fail 'Installation must belong to the current user.'
mkdir "$prefix/.install-lock" 2>/dev/null || fail 'Another installation is active; inspect the lock if an earlier installer was interrupted.'
stage=
cleanup() { [ -z "$stage" ] || rm -rf "$stage"; rmdir "$prefix/.install-lock" 2>/dev/null || true; }
trap cleanup EXIT HUP INT TERM
marker="$prefix/.android-publisher-cli"
[ ! -L "$marker" ] || fail 'Unexpected installation marker symlink.'
if [ -e "$marker" ]; then
 [ -f "$marker" ] && [ "$(cat "$marker")" = android-publisher-cli ] || fail 'Unrecognized installation marker.'
fi
if [ "$action" = uninstall ]; then
 [ -f "$marker" ] && [ ! -L "$marker" ] || fail 'No managed installation found.'
 [ ! -L "$prefix/bin" ] || fail 'Unexpected bin symlink.'
 if [ -L "$prefix/bin/release" ] && [ "$(readlink "$prefix/bin/release")" = ../current/bin/release ]; then rm "$prefix/bin/release"; fi
 [ ! -L "$prefix/current" ] || rm "$prefix/current"
 printf '%s\n' 'Command unlinked. Version files, publisher data and Keychain were preserved.'
 exit 0
fi
mkdir -p "$prefix/versions" "$prefix/bin"
[ ! -L "$prefix/versions" ] && [ ! -L "$prefix/bin" ] || fail 'Unexpected managed-directory symlink.'
if [ "$action" = rollback ]; then version=$rollback_version; else version=$(cat "$bundle_dir/VERSION"); fi
case "$version" in ''|*[!0-9A-Za-z.+-]*) fail 'Invalid version.' ;; esac
[ "${#version}" -le 100 ] || fail 'Version must not exceed 100 characters.'
printf '%s\n' "$version" | LC_ALL=C /usr/bin/grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$' || fail 'Invalid version.'
core=${version%%+*}
case "$core" in
 *-*) printf '%s\n' "${core#*-}" | /usr/bin/awk -F. '{ for (i=1; i<=NF; i++) if ($i ~ /^0[0-9]+$/) exit 1 }' || fail 'Invalid version.' ;;
esac
manifest_paths() {
 printf '%s\n' BUILD.json CHANGELOG.md DISTRIBUTION.txt LICENSE README.md THIRD_PARTY_LICENSES.txt THIRD_PARTY_NOTICES.md VERSION bin/release docs/channels/automatic-observation.md docs/channels/protocol-evidence.md docs/release-process.md docs/colleague-quickstart.md docs/daily-cli-workflow.md docs/delivery.md install.sh
}
verify() {
 target=$1
 [ -d "$target" ] && [ ! -L "$target" ] || fail 'Bundle must be a regular directory.'
 [ -z "$(/usr/bin/find "$target" ! -type f ! -type d -print)" ] || fail 'Bundle contains a symlink or special file.'
 expected=$(manifest_paths | LC_ALL=C /usr/bin/sort)
 actual=$(cd "$target" && /usr/bin/find . -type f -print | /usr/bin/sed 's|^./||' | /usr/bin/grep -v '^SHA256SUMS$' | LC_ALL=C /usr/bin/sort)
 [ "$actual" = "$expected" ] || fail 'Bundle contains missing or unexpected files.'
 [ -f "$target/SHA256SUMS" ] && [ ! -L "$target/SHA256SUMS" ] || fail 'Bundle checksum manifest is missing.'
 listed=$(/usr/bin/awk 'length($1) != 64 || $1 !~ /^[0-9a-f]+$/ || NF != 2 { exit 1 } { print $2 }' "$target/SHA256SUMS") || fail 'Invalid checksum manifest.'
 [ "$(printf '%s\n' "$listed" | LC_ALL=C /usr/bin/sort)" = "$expected" ] || fail 'Checksum manifest does not match the allowed bundle files.'
 (cd "$target" && /usr/bin/shasum -a 256 -c SHA256SUMS) || fail 'Bundle checksum mismatch.'
 [ "$(cat "$target/VERSION")" = "$version" ] || fail 'Bundle version does not match the selected version.'
 for field in version os arch; do
  value=$(/usr/bin/plutil -extract "$field" raw -o - "$target/BUILD.json") || fail 'Invalid build metadata.'
  case "$field:$value" in "version:$version"|os:darwin|arch:arm64) ;; *) fail 'Build metadata does not match the selected Mac package.' ;; esac
 done
 /usr/bin/codesign --verify --strict "$target/bin/release" || fail 'Code signature validation failed.'
 signature=$(/usr/bin/codesign -dv --verbose=2 "$target/bin/release" 2>&1)
 printf '%s\n' "$signature" | /usr/bin/grep -qx 'Identifier=io.github.wangyiliangnet.android-publisher-cli.release' || fail 'Unexpected executable identifier.'
 distribution=$(cat "$target/DISTRIBUTION.txt")
 case "$distribution" in
  'mode=ci-preview
signature=ad-hoc
notarized=no
trusted_identity=none
Test preview. No stable trusted Keychain identity; not notarized.')
   printf '%s\n' "$signature" | /usr/bin/grep -qx 'Signature=adhoc' || fail 'CI preview signature does not match its declared mode.' ;;
  'mode=local
signature=certificate
notarized=no
Local certificate-signed preview; notarization has not been verified.')
   printf '%s\n' "$signature" | /usr/bin/grep -q '^Authority=' || fail 'Local distribution requires a certificate signature.' ;;
  'mode=local
signature=certificate
notarized=accepted-at-package-build
Developer ID signed; Apple notarization accepted at package build.')
   printf '%s\n' "$signature" | /usr/bin/grep -q '^Authority=Developer ID Application:' || fail 'Declared notarization requires Developer ID Application.'
   case "$signature" in *'flags=0x10000(runtime)'*) ;; *) fail 'Declared notarization requires hardened runtime.' ;; esac ;;
  *) fail 'Missing or unrecognized distribution metadata.' ;;
 esac
 # Base macOS tools only: lipo would require Xcode Command Line Tools.
 header=$(/usr/bin/od -An -t x4 -N8 "$target/bin/release" | /usr/bin/tr -d '[:space:]')
 [ "$header" = feedfacf0100000c ] || fail 'Unexpected executable architecture.'
 [ "$("$target/bin/release" version)" = "$version" ] || fail 'Executable version does not match the bundle.'
 "$target/bin/release" version --json | /usr/bin/cmp - "$target/BUILD.json" >/dev/null || fail 'Executable build metadata does not match the bundle.'
}
if [ "$action" = install ]; then
 verify "$bundle_dir"
 if [ -e "$prefix/versions/$version" ] || [ -L "$prefix/versions/$version" ]; then
  [ ! -L "$prefix/versions/$version" ] || fail 'Unexpected version symlink.'
  verify "$prefix/versions/$version"
  cmp "$bundle_dir/SHA256SUMS" "$prefix/versions/$version/SHA256SUMS" >/dev/null || fail 'This version already exists with different content; use a new version.'
 else
  stage=$(mktemp -d "$prefix/.install.XXXXXX")
  /usr/bin/ditto "$bundle_dir" "$stage"
  verify "$stage"
  mv "$stage" "$prefix/versions/$version"
  stage=
 fi
else
 [ -f "$marker" ] || fail 'No managed installation found.'
 verify "$prefix/versions/$version"
fi
[ ! -e "$prefix/bin/release" ] || [ -L "$prefix/bin/release" ] || fail 'An unrelated release command already occupies the destination.'
if [ -L "$prefix/bin/release" ]; then [ "$(readlink "$prefix/bin/release")" = ../current/bin/release ] || fail 'Unrelated release symlink.'; fi
[ ! -e "$prefix/current" ] || [ -L "$prefix/current" ] || fail 'Unrelated current directory.'
[ ! -e "$prefix/.current-new" ] && [ ! -L "$prefix/.current-new" ] || fail 'An interrupted switch needs inspection: .current-new already exists.'
ln -s "versions/$version" "$prefix/.current-new"
# macOS mv -h replaces the current symlink without following its target.
mv -fh "$prefix/.current-new" "$prefix/current"
if [ ! -L "$prefix/bin/release" ]; then ln -s ../current/bin/release "$prefix/bin/release"; fi
printf '%s\n' 'android-publisher-cli' > "$marker"
printf 'Installed %s\nAdd this directory to PATH: %s/bin\nPublisher data and credentials stay on this Mac.\n' "$version" "$prefix"
