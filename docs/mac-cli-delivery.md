# Mac delivery

The supported distribution target is Apple Silicon macOS (`darwin-arm64`). The executable is `release`; a packaged install needs no Go or Node runtime for API commands. APK verification needs Android SDK Build Tools and a JDK. Browser acceptance requires the pinned development runtimes and is not a daily production login requirement.

## Distribution modes

| Package | Purpose | Signing and trust |
| --- | --- | --- |
| `android-publisher-cli-VERSION-darwin-arm64-ci-preview.tar.gz` | CI acceptance and public beta preview | Ad-hoc signed; unnotarized; no stable trusted Keychain identity |
| `android-publisher-cli-VERSION-darwin-arm64.tar.gz` | Locally configured fixed-identity distribution | Requires an explicitly configured signing identity; notarization is a separate opt-in capability |

Read each release's distribution notes before installation. A checksum detects changed bytes, and signature verification checks the bundle's signature; neither makes an ad-hoc preview an Apple-trusted distribution. CI preview builds do not consume a developer certificate, reuse a local certificate configuration, or contain production credentials.

Use a fixed signed identity before granting persistent access to production Keychain entries. Rebuilding with changing ad-hoc identities can require repeated macOS authorization. Formal distribution needs the team's own Developer ID Application identity, appropriate signing settings, and Apple notarization credentials. This repository does not supply them or claim that every release is notarized.

## Install, upgrade, rollback, uninstall

Download the archive and its `.sha256` file together, verify the checksum in their directory, extract it, and run the bundled installer:

```sh
shasum -a 256 -c android-publisher-cli-VERSION-darwin-arm64-ci-preview.tar.gz.sha256
tar -xzf android-publisher-cli-VERSION-darwin-arm64-ci-preview.tar.gz
cd android-publisher-cli-VERSION-darwin-arm64-ci-preview
/bin/sh install.sh
```

Replace `VERSION` with the release version. The default prefix is `~/.local/android-publisher-cli/`; an optional `--prefix "/absolute/dedicated/directory"` selects another user-owned directory. Add the prefix's `bin` directory to PATH.

The installer checks the archive's file hashes, code signature, executable architecture, ownership, and unsafe symlinks. It keeps version directories and switches a `current` link. Installing an existing version with different content is rejected; choose a new version.

Upgrade by running the new bundle's installer with the same prefix. Stop active publishing and query commands before changing executables. To switch back to an installed version, run an installer from the matching managed bundle:

```sh
/bin/sh install.sh --rollback 0.1.0-beta.1
/bin/sh install.sh --uninstall
```

Uninstall removes managed command links and markers, retaining version files and publishing data. It does not erase Keychain entries. CLI rollback changes the executable only; it does not downgrade state schemas or bypass recovery/version guards. Check compatibility before rolling back after a state change.

## Build a local package

On Apple Silicon macOS, install the pinned Go version, configure a fixed signing identity using the signing tools, and run:

```sh
PUBLISHER_RELEASE_VERSION=0.1.0-beta.1 make package-macos
```

Default packaging fails if its required signing identity is unavailable; it does not silently fall back to an ad-hoc signature. For synthetic CI preview acceptance only:

```sh
PUBLISHER_PACKAGE_MODE=ci-preview \
PUBLISHER_RELEASE_VERSION=0.1.0-beta.1 make package-macos
```

The archive contains an explicit allowlist: executable, installer, selected usage documents, build/version/distribution metadata, checksums, MIT license, and dependency notices. It does not include a source checkout, APKs, runtime state, account configuration, browser profiles, or Keychain contents.

## Acceptance and provenance

The release workflow reruns the required checks and builds two synthetic preview versions to exercise installation, upgrade, rollback, uninstall, repeated installation, changed bytes, and unsafe symlinks. It produces a draft release with version/commit information and checksums. A maintainer reviews the draft and publishes it manually; no app-store submission occurs in that workflow.

Retain third-party notices with redistributed packages. Dependencies keep their licenses; the project's MIT license does not replace those terms. See [the release process](release-process.md) for provenance and publication gates.
