#!/bin/sh
# Official Android download page pins this native cmdline-tools 22.0 archive.
set -eu
[ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ] || { printf '%s\n' 'Android CI requires macOS arm64.' >&2; exit 1; }
: "${RUNNER_TEMP:?Set RUNNER_TEMP to an isolated temporary directory.}"
: "${JAVA_HOME:?Install the pinned JDK first.}"
sdk_root="$RUNNER_TEMP/publisher-android-sdk"
[ ! -e "$sdk_root" ] || { printf '%s\n' 'Refusing to reuse an existing SDK directory.' >&2; exit 1; }
mkdir -p "$sdk_root/cmdline-tools"
archive="$RUNNER_TEMP/publisher-commandlinetools.zip"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --retry 3 'https://dl.google.com/android/repository/commandlinetools-mac_arm64-15859902_latest.zip' -o "$archive"
printf '%s  %s\n' 835b62a26162b229b441d1f6d4680383815a270809eb33522c0d480fa5002c4e "$archive" | shasum -a 256 -c - >/dev/null
unzip -q "$archive" -d "$sdk_root/cmdline-tools"
mv "$sdk_root/cmdline-tools/cmdline-tools" "$sdk_root/cmdline-tools/22.0"
answers="$RUNNER_TEMP/publisher-android-license-answers"
python3 -c 'print("y\n" * 100)' > "$answers"
manager="$sdk_root/cmdline-tools/22.0/bin/sdkmanager"
"$manager" --sdk_root="$sdk_root" --licenses < "$answers" > /dev/null
"$manager" --sdk_root="$sdk_root" --install 'platforms;android-36' 'build-tools;36.1.0' < "$answers"
rm -f "$answers" "$archive"
"$sdk_root/build-tools/36.1.0/aapt2" version
"$sdk_root/build-tools/36.1.0/apksigner" version
if [ -n "${GITHUB_ENV:-}" ]; then
    printf 'PUBLISHER_TEST_SDK_ROOT=%s\nPUBLISHER_TEST_JAVA_HOME=%s\n' "$sdk_root" "$JAVA_HOME" >> "$GITHUB_ENV"
fi
printf '%s\n' 'Installed isolated Android platform 36 / build-tools 36.1.0 / cmdline-tools 22.0.'
