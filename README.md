# Android Publisher CLI

[![CI](https://github.com/wangyiliangnet/android-publisher-cli/actions/workflows/check.yml/badge.svg?branch=main)](https://github.com/wangyiliangnet/android-publisher-cli/actions/workflows/check.yml)

A local publishing CLI for Apple Silicon Macs, covering Huawei AppGallery, HONOR, Xiaomi, OPPO, vivo, and Tencent MyApp (应用宝).

Prepare signed APKs, review one immutable publishing plan, submit selected channels, and inspect release history from the same Mac. Credentials stay in macOS Keychain. The executable is named `release`.

**Status: public beta.** Protocols and recovery behavior have synthetic acceptance tests. This repository does not certify any developer account, application, store permission, or live release. Start with a test application and review each publishing plan before allowing business writes.

## Start here

- [中文同事使用手册](docs/colleague-quickstart.md)
- [Daily workflow and recovery](docs/daily-cli-workflow.md)
- [Mac installation and distribution](docs/mac-cli-delivery.md)
- [Channel capabilities and evidence](docs/channels/protocol-evidence.md)
- [Architecture](docs/architecture.md), [roadmap](docs/execution-plan.md), and [contributing](CONTRIBUTING.md)

An Apple Silicon Mac, Android SDK Build Tools, and a compatible JDK are needed to inspect real APKs. A packaged CLI needs no Go or Node.js installation for API publishing. The browser observer is currently a simulation and acceptance-test facility, not a production login prerequisite.

```sh
release version --json
release doctor
release setup --project demo --apk "/absolute/path/trusted.apk"
```

The setup wizard uses Chinese prompts and saves public account identifiers and Keychain entry names. Import secrets separately using hidden terminal input or a private credential file. Never paste credentials into issues, pull requests, or command-line arguments.

For each release, prepare explicit APKs and notes, run the read-only preflight, and review a saved approval file. Submission requires the exact reviewed approval digest. [The workflow guide](docs/daily-cli-workflow.md) includes the full sequence.

## What it preserves

- APK, signer, notes, target, and publication policy are frozen before publishing writes.
- Operation intent is saved before a request; a verified receipt is saved immediately afterward.
- Lost responses remain unresolved until reconciliation supplies adequate evidence. Restarting does not justify sending the write again.
- Execution, review, store availability, and query health are separate states. “Accepted” does not mean “approved” or “live.”
- Xiaomi review inference and unassociated MyApp facts are labeled separately and never count as verified target approval or availability.
- Cached history and report export work offline. Routine status refresh uses APIs and MyApp's public store page without requiring browser login or manual status entry.

Local state lives under `~/Library/Application Support/AndroidPublisherCLI/`; credential services use `io.github.wangyiliangnet.android-publisher-cli.v1`. Runtime files, signed URLs, credentials, and APKs do not belong in Git.

## Build and validate

Use the exact Go and Node.js versions in [`.go-version`](.go-version) and [`.node-version`](.node-version), plus the npm version in [the browser package](browser/package.json).

```sh
make browser-install
PUBLISHER_REQUIRE_ANDROID_TESTS=1 make check
```

`make check` runs formatting, static checks, race-enabled Go tests, browser acceptance tests, and a local build. Android signature integration tests need the SDK and JDK. CI supplies them and requires these checks rather than silently skipping them. Tests use synthetic inputs and temporary credentials, never production store accounts or real application APKs.

See [the release process](docs/release-process.md) for branch protection, required CI, version tags, and draft release review.

## License

Original project code is available under the [MIT License](LICENSE). Dependencies retain their own licenses. Vendor services and documentation have their own terms. The project is independent of the store vendors. See [protocol evidence](docs/channels/protocol-evidence.md) for source and implementation boundaries.
