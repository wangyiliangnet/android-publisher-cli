#!/bin/sh
# Pinned public audit tools. Digests are from upstream GitHub release assets.
set -eu
[ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ] || { printf '%s\n' 'CI tools require macOS arm64.' >&2; exit 1; }
: "${RUNNER_TEMP:?Set RUNNER_TEMP to an isolated temporary directory.}"
tools_dir="$RUNNER_TEMP/publisher-ci-tools"
mkdir -p "$tools_dir"
fetch() {
    name=$1
    url=$2
    digest=$3
    archive="$tools_dir/$name.tar.gz"
    curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --retry 3 "$url" -o "$archive"
    printf '%s  %s\n' "$digest" "$archive" | shasum -a 256 -c - >/dev/null
    tar -xzf "$archive" -C "$tools_dir" "$name"
    chmod 0755 "$tools_dir/$name"
}
fetch gitleaks 'https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/gitleaks_8.30.1_darwin_arm64.tar.gz' b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5
fetch actionlint 'https://github.com/rhysd/actionlint/releases/download/v1.7.12/actionlint_1.7.12_darwin_arm64.tar.gz' aba9ced2dee8d27fecca3dc7feb1a7f9a52caefa1eb46f3271ea66b6e0e6953f
if [ -n "${GITHUB_PATH:-}" ]; then printf '%s\n' "$tools_dir" >> "$GITHUB_PATH"; fi
printf '%s\n' 'Verified Gitleaks 8.30.1 and actionlint 1.7.12.'
