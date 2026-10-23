#!/bin/sh
set -eu
archive=${1:?Provide a built tar.gz archive}
previous=${2:-}
fail() { printf '%s\n' "$1" >&2; exit 1; }
case "$archive" in /*) ;; *) archive="$(pwd)/$archive" ;; esac
workspace=$(mktemp -d /private/tmp/publisher-cli-install-test.XXXXXX)
trap 'rm -rf "$workspace"' EXIT HUP INT TERM
extract() {
 input=$1; destination=$2
 [ -f "$input.sha256" ] || fail 'Archive checksum sidecar is missing.'
 (cd "$(dirname "$input")" && /usr/bin/shasum -a 256 -c "$(basename "$input").sha256") || fail 'Archive checksum mismatch.'
 /usr/bin/tar -tzf "$input" | /usr/bin/awk '
  /^\// || /(^|\/)\.\.?($|\/)/ || /[^0-9A-Za-z._+\/-]/ { exit 1 }
  { split($0, parts, "/"); if (parts[1] !~ /^android-publisher-cli-[0-9]/) exit 1; if (root != "" && root != parts[1]) exit 1; root=parts[1]; count++ }
  END { if (count == 0) exit 1 }
 ' || fail 'Archive contains an unsafe path.'
 /usr/bin/tar -tvzf "$input" | /usr/bin/awk 'substr($0,1,1) != "-" && substr($0,1,1) != "d" { exit 1 }' || fail 'Archive contains a link or special file.'
 /usr/bin/tar -xzf "$input" -C "$destination"
}
extract "$archive" "$workspace"
set -- "$workspace"/android-publisher-cli-*
[ "$#" = 1 ] || fail 'Archive must contain exactly one bundle.'
bundle=$1
[ -f "$bundle/install.sh" ] || fail 'Missing installer.'
version=$(cat "$bundle/VERSION")
# Documentation shipped for offline use must not link to missing local pages.
for document in "$bundle"/*.md "$bundle"/docs/*.md "$bundle"/docs/channels/*.md; do
 /usr/bin/grep -oE '\]\([^)]*\)' "$document" | /usr/bin/sed -e 's/^](//' -e 's/)$//' | while IFS= read -r link; do
  case "$link" in https://*|http://*|mailto:*|\#*) continue ;; esac
  path=${link%%#*}
  [ -f "$(dirname "$document")/$path" ] || fail "Missing packaged documentation link: $path"
 done
done
prefix="$workspace/installed"
old_version=
old_digest=
if [ -n "$previous" ]; then
 case "$previous" in /*) ;; *) previous="$(pwd)/$previous" ;; esac
 mkdir "$workspace/previous"
 extract "$previous" "$workspace/previous"
 set -- "$workspace/previous"/android-publisher-cli-*
 [ "$#" = 1 ] || fail 'Previous archive must contain one bundle.'
 old_bundle=$1
 old_version=$(cat "$old_bundle/VERSION")
 [ "$old_version" != "$version" ] || fail 'Upgrade smoke requires two different versions.'
 /bin/sh "$old_bundle/install.sh" --prefix "$prefix"
 [ "$("$prefix/bin/release" version)" = "$old_version" ]
 old_digest=$(/usr/bin/shasum -a 256 "$old_bundle/bin/release" | /usr/bin/awk '{print $1}')
 [ "$(/usr/bin/shasum -a 256 "$prefix/bin/release" | /usr/bin/awk '{print $1}')" = "$old_digest" ]
fi
/bin/sh "$bundle/install.sh" --prefix "$prefix"
[ "$("$prefix/bin/release" version)" = "$version" ]
digest=$(/usr/bin/shasum -a 256 "$bundle/bin/release" | /usr/bin/awk '{print $1}')
assert_current() {
 [ "$("$prefix/bin/release" version)" = "$version" ]
 [ "$(/usr/bin/shasum -a 256 "$prefix/bin/release" | /usr/bin/awk '{print $1}')" = "$digest" ]
}
assert_current
if [ -n "$old_version" ]; then
 /bin/sh "$bundle/install.sh" --prefix "$prefix" --rollback "$old_version"
 [ "$("$prefix/bin/release" version)" = "$old_version" ]
 [ "$(/usr/bin/shasum -a 256 "$prefix/bin/release" | /usr/bin/awk '{print $1}')" = "$old_digest" ]
 /bin/sh "$bundle/install.sh" --prefix "$prefix" --rollback "$version"
 assert_current
fi
"$prefix/bin/release" version --json
"$prefix/bin/release" init --file "$workspace/release-input.json"
/bin/sh "$bundle/install.sh" --prefix "$prefix"
/bin/sh "$bundle/install.sh" --prefix "$prefix" --rollback "$version"
# Corrupt bytes, undeclared content, symlinks, and same-version metadata changes
# must fail while preserving the installed executable.
/usr/bin/ditto "$bundle" "$workspace/corrupt"
printf '%s\n' 'tampered' >> "$workspace/corrupt/bin/release"
if /bin/sh "$workspace/corrupt/install.sh" --prefix "$prefix" > "$workspace/rejected.log" 2>&1; then fail 'Tampered bundle was accepted.'; fi
assert_current
/usr/bin/ditto "$bundle" "$workspace/collision"
printf '%s\n' 'Changed documentation for an existing version.' >> "$workspace/collision/README.md"
(cd "$workspace/collision" && /usr/bin/shasum -a 256 bin/release install.sh README.md docs/delivery.md docs/colleague-quickstart.md docs/daily-cli-workflow.md docs/channels/automatic-observation.md docs/channels/protocol-evidence.md docs/release-process.md CHANGELOG.md LICENSE THIRD_PARTY_NOTICES.md VERSION BUILD.json DISTRIBUTION.txt THIRD_PARTY_LICENSES.txt > SHA256SUMS)
if /bin/sh "$workspace/collision/install.sh" --prefix "$prefix" > "$workspace/collision.log" 2>&1; then fail 'Same-version bundle with changed documentation was accepted.'; fi
assert_current
/usr/bin/ditto "$bundle" "$workspace/extra"
printf '%s\n' 'unexpected' > "$workspace/extra/unexpected.txt"
if /bin/sh "$workspace/extra/install.sh" --prefix "$prefix" > "$workspace/extra.log" 2>&1; then fail 'Unexpected payload file was accepted.'; fi
assert_current
/usr/bin/ditto "$bundle" "$workspace/link-payload"
ln -s README.md "$workspace/link-payload/extra-link"
if /bin/sh "$workspace/link-payload/install.sh" --prefix "$prefix" > "$workspace/payload-link.log" 2>&1; then fail 'Symlink payload was accepted.'; fi
assert_current
ln -s "$prefix" "$workspace/alias"
if /bin/sh "$bundle/install.sh" --prefix "$workspace/alias" > "$workspace/symlink.log" 2>&1; then fail 'Symlink installation prefix was accepted.'; fi
assert_current
/bin/sh "$bundle/install.sh" --prefix "$prefix" --uninstall
[ ! -e "$prefix/bin/release" ]
[ -f "$prefix/versions/$version/bin/release" ]
printf '%s\n' 'Package install, upgrade, rollback, complete-version collision, corruption, payload allowlist, symlink rejection and unlink-only uninstall passed.'
