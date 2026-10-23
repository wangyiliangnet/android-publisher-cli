# Branch, merge, and release process

The default branch is `main`. A topic branch becomes a pull request, required CI passes on the up-to-date merge candidate, review conversations are resolved, and the change is squash-merged. A version tag is created only by the explicit draft release workflow after its own validation.

## Branches and merge gates

- Branch from current `main`: `feat/topic`, `fix/topic`, `docs/topic`, or `codex/topic` for automated work.
- Open a PR against `main`; describe behavior, validation, compatibility/recovery implications, protocol sources, and public-data checks as relevant.
- The main ruleset requires a PR, the stable CI check named **`check`**, an up-to-date branch, resolved conversations, and squash merge. Force pushes and branch deletion are blocked.
- This project initially has one human maintainer, so the mandatory GitHub approval count is zero. Use independent review for sensitive changes; subagent review is supporting evidence, not a GitHub human approval. Require at least one human approval when a second maintainer joins.
- Delete merged topic branches. Never merge a failing or stale check by treating an unrelated earlier run as evidence.

These rules govern the CLI repository. Merging a PR never authorizes an Android store upload or submission.

## CI contract

The `CI` workflow runs on pull requests, pushes to `main`, and manual dispatch. Its required job is `check`. It uses the standard Apple Silicon `macos-15` runner and pinned Go, Node.js/npm, Java, Android platform/build-tools, and Chromium dependencies.

The check runs `PUBLISHER_REQUIRE_ANDROID_TESTS=1 make check`. This includes formatting, Go static checks, race-enabled tests, Android signature integration, real Chromium acceptance with local fixtures, subprocess recovery coverage, and a build. Required test families must fail when a prerequisite is missing instead of quietly skipping. Workflow/script checks cover the automation itself.

Actions are pinned to full upstream commit SHAs. Normal checks have read-only repository permissions, bounded run time, and cancellation of superseded runs. Tests use synthetic data and temporary test identities; no channel credential, real application APK, production state, Apple signing certificate, or personal browser session is required.

## Version policy

The first public beta is `0.1.0-beta.1`. Use SemVer-style versions without a leading `v` as workflow input; the release tag is `vVERSION`. Development builds report a development version and commit when available. Never replace a published tag or reuse a package version for different bytes.

Update [CHANGELOG.md](../CHANGELOG.md) through a PR before preparing a release. Document user-visible changes, migration requirements, known limitations, and distribution/signing status. Beta versioning does not remove the obligation to explain state compatibility.

## Prepare a draft release

1. Merge the release notes and fixes through the normal PR gate. Verify `main` is at the intended commit and its required CI is green.
2. In GitHub Actions, manually run the release workflow on current `main`, entering a version such as `0.1.0-beta.1` without the `v` prefix.
3. The workflow validates the version and ref, reruns checks, builds an ad-hoc signed arm64 CI preview, and performs two-version package installation/upgrade/rollback/uninstall and tamper/symlink rejection acceptance. It creates the version tag and a **draft** GitHub release only after success.
4. Review the draft's exact tag/commit, package metadata, checksums, license/notices, installation results, and notes. Inspect the archive allowlist. Confirm it contains no account files, secrets, application APKs, runtime state, raw responses, or private documentation.
5. Confirm the release explicitly states **CI preview, ad-hoc signed, unnotarized, no stable trusted Keychain identity**. Mark beta releases as prereleases. Download and verify the published candidate bytes if the draft is to be distributed.
6. Publish the draft manually after this final review. The workflow does not publish a public release automatically and never submits an Android app to a store.

Only the release job receives the repository permission required to create a tag and draft release. CI preview mode is isolated from local certificate configuration and refuses Developer ID/notarization options. Production store credentials must never be added to this workflow.

If draft creation or an asset upload is interrupted, use **Re-run failed jobs**
on the original workflow run while `main` still points to the tested commit.
This preserves that run's tested package bytes. A fresh dispatch or a rebuild
can produce different archive bytes and will be rejected if a matching draft
already exists. The workflow checks the
existing lightweight tag, exact draft metadata and expected asset digests,
downloads existing assets to verify their bytes, and uploads only missing files.
It rejects a published release, a different tag target, conflicting metadata or
asset bytes, and unexpected assets. It never replaces a tag or existing asset.
If `main` has moved or a conflict remains, investigate and prepare a new version
through the normal process.

## Fixed-identity distribution

For use with production Keychain entries, prepare a separately configured fixed signing identity and validate it on the intended Macs. Developer ID signing and Apple notarization require the team's Apple developer credentials and explicit local/release configuration; they are not implied by a green preview job. See [Mac delivery](mac-cli-delivery.md).

If candidate validation fails, fix the cause through a PR and build a new candidate. After publication, ship a new version rather than changing an existing artifact. CLI rollback switches an installed executable; it does not undo store writes or bypass local state-schema guards.
