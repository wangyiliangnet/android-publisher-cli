#!/bin/sh
set -eu
fail() { printf '%s\n' "$1" >&2; exit 1; }
[ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ] || fail 'Packaging requires Apple Silicon macOS.'
version=${PUBLISHER_RELEASE_VERSION:-0.1.0-beta.1}
case "$version" in ''|*[!0-9A-Za-z.+-]*) fail 'Invalid release version.' ;; esac
[ "${#version}" -le 100 ] || fail 'Version must not exceed 100 characters.'
printf '%s\n' "$version" | LC_ALL=C /usr/bin/grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$' || fail 'Invalid release version.'
core=${version%%+*}
case "$core" in
 *-*) printf '%s\n' "${core#*-}" | /usr/bin/awk -F. '{ for (i=1; i<=NF; i++) if ($i ~ /^0[0-9]+$/) exit 1 }' || fail 'Invalid release version.' ;;
esac
mode=${PUBLISHER_PACKAGE_MODE:-local}
case "$mode" in
 local) suffix=; executable=bin/release ;;
 ci-preview)
  [ "${PUBLISHER_DEVELOPER_ID:-0}" = 0 ] && [ -z "${PUBLISHER_NOTARY_PROFILE:-}" ] || fail 'CI previews cannot use Developer ID or notarization.'
  suffix=-ci-preview; executable=bin/release-ci-preview ;;
 *) fail 'Package mode must be local or ci-preview.' ;;
esac
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
name="android-publisher-cli-$version-darwin-arm64$suffix"
mkdir -p dist
for output in "dist/$name.tar.gz" "dist/$name.tar.gz.sha256" "dist/$name.notary.json"; do
 [ ! -e "$output" ] && [ ! -L "$output" ] || fail 'Distribution version already exists; choose a new version.'
done
PUBLISHER_RELEASE_VERSION=$version PUBLISHER_PACKAGE_MODE=$mode PUBLISHER_REQUIRE_SIGNING=1 /bin/sh tools/build-release.sh "${1:-go}"
stage=$(mktemp -d dist/.package.XXXXXX)
trap 'rm -rf "$stage"' EXIT HUP INT TERM
bundle="$stage/$name"
mkdir -p "$bundle/bin" "$bundle/docs/channels"
# Explicit allowlist: no runtime directories, accounts, APKs or source checkout.
/usr/bin/ditto "$executable" "$bundle/bin/release"
cp tools/install-macos.sh "$bundle/install.sh"
cat > "$bundle/README.md" <<'DOCS'
# Android 发布工具：离线文档

面向使用 Apple Silicon Mac 的发版同事：

1. 第一次使用或升级：[安装与本机配置](docs/colleague-quickstart.md)。
2. 项目已配置：[日常发布、查询与恢复](docs/daily-cli-workflow.md)。
3. 理解查询结果：[状态解释](docs/channels/automatic-observation.md)。

安装前阅读随包 DISTRIBUTION.txt，确认该产物的签名和公证状态。
DOCS
cp docs/mac-cli-delivery.md "$bundle/docs/delivery.md"
cp docs/colleague-quickstart.md "$bundle/docs/colleague-quickstart.md"
cp docs/daily-cli-workflow.md "$bundle/docs/daily-cli-workflow.md"
cp docs/channels/automatic-observation.md "$bundle/docs/channels/automatic-observation.md"
cp docs/channels/protocol-evidence.md "$bundle/docs/channels/protocol-evidence.md"
sed 's|(mac-cli-delivery.md)|(delivery.md)|g' docs/release-process.md > "$bundle/docs/release-process.md"
sed 's|(docs/mac-cli-delivery.md)|(docs/delivery.md)|g' CHANGELOG.md > "$bundle/CHANGELOG.md"
cp LICENSE "$bundle/LICENSE"
cp THIRD_PARTY_NOTICES.md "$bundle/THIRD_PARTY_NOTICES.md"
printf '%s\n' "$version" > "$bundle/VERSION"
"./$executable" version --json > "$bundle/BUILD.json"
if [ "$mode" = ci-preview ]; then
 printf '%s\n' 'mode=ci-preview' 'signature=ad-hoc' 'notarized=no' 'trusted_identity=none' 'Test preview. No stable trusted Keychain identity; not notarized.' > "$bundle/DISTRIBUTION.txt"
else
 printf '%s\n' 'mode=local' 'signature=certificate' 'notarized=no' 'Local certificate-signed preview; notarization has not been verified.' > "$bundle/DISTRIBUTION.txt"
fi
if [ -n "${PUBLISHER_NOTARY_PROFILE:-}" ]; then
 identity=$(/usr/bin/codesign -dv "$executable" 2>&1)
 case "$identity" in *'Authority=Developer ID Application:'*) ;; *) fail 'Notarization requires Developer ID Application.' ;; esac
 case "$identity" in *'flags=0x10000(runtime)'*) ;; *) fail 'Notarization requires hardened-runtime signing.' ;; esac
 printf '%s\n' 'mode=local' 'signature=certificate' 'notarized=accepted-at-package-build' 'Developer ID signed; Apple notarization accepted at package build.' > "$bundle/DISTRIBUTION.txt"
fi
notices="$bundle/THIRD_PARTY_LICENSES.txt"
printf '%s\n' 'Go standard library' > "$notices"
cat "$("${1:-go}" env GOROOT)/LICENSE" >> "$notices"
"${1:-go}" list -m -f '{{.Path}}|{{.Version}}|{{.Dir}}' all | while IFS='|' read -r module module_version module_dir; do
 [ -n "$module_version" ] && [ -n "$module_dir" ] || continue
 printf '\nModule: %s %s\n' "$module" "$module_version" >> "$notices"
 found=0
 for license_file in "$module_dir"/LICENSE* "$module_dir"/COPYING* "$module_dir"/NOTICE* "$module_dir"/AUTHORS*; do
  [ -f "$license_file" ] && [ ! -L "$license_file" ] || continue
  printf '\n%s\n' "$(basename "$license_file")" >> "$notices"
  cat "$license_file" >> "$notices"
  found=1
 done
 [ "$found" = 1 ] || { printf 'Missing license: %s\n' "$module" >&2; exit 1; }
done
(cd "$bundle" && /usr/bin/shasum -a 256 bin/release install.sh README.md docs/delivery.md docs/colleague-quickstart.md docs/daily-cli-workflow.md docs/channels/automatic-observation.md docs/channels/protocol-evidence.md docs/release-process.md CHANGELOG.md LICENSE THIRD_PARTY_NOTICES.md VERSION BUILD.json DISTRIBUTION.txt THIRD_PARTY_LICENSES.txt > SHA256SUMS)
COPYFILE_DISABLE=1 /usr/bin/tar -czf "$stage/$name.tar.gz" -C "$stage" "$name"
if [ -n "${PUBLISHER_NOTARY_PROFILE:-}" ]; then
 /usr/bin/ditto -c -k --keepParent "$bundle" "$stage/$name.zip"
 /usr/bin/xcrun notarytool submit "$stage/$name.zip" --keychain-profile "$PUBLISHER_NOTARY_PROFILE" --wait --output-format json > "$stage/notary.json"
 /usr/bin/plutil -extract status raw -o - "$stage/notary.json" | /usr/bin/grep -qx Accepted || fail 'Notarization was not accepted.'
 cp "$stage/notary.json" "dist/$name.notary.json"
 # Raw command executables cannot be stapled; Gatekeeper retrieves tickets online.
fi
mv "$stage/$name.tar.gz" "dist/$name.tar.gz"
(cd dist && /usr/bin/shasum -a 256 "$name.tar.gz" > "$name.tar.gz.sha256")
printf 'Bundle: %s/dist/%s.tar.gz\n' "$root" "$name"
