# Public beta acceptance

This record covers the public CLI, using synthetic inputs on Apple Silicon macOS.
It does not certify a store account, application, production submission, or Apple
notarization.

## Local acceptance

- Complete `make check` with pinned Go and Node.js: formatting, vet, Go race,
  real Chromium, temporary native Keychain, interrupted subprocess recovery,
  and a build passed.
- Mandatory Android integration passed with an isolated Android SDK, real
  Build Tools, a JDK, and generated test APKs and signing keys.
- Two-version CI preview packaging passed installation, upgrade, rollback,
  repeated installation, complete-version collision, checksum corruption,
  unexpected files, symlinks, offline documentation, and uninstall checks.
- Separate runtime, Keychain service, signing identifier, and installation
  namespaces were checked. Tests and preview builds use no production accounts.
- Workflow lint, release version guards, script guards, privacy regression
  tests, exact secret-scanner fixture allowances, and public-history scans are
  required before publication.

## Independent review

Read-only reviewers covered public-data and license boundaries, CI and repository
rules, and Mac delivery. Material findings were:

1. The quickstart PATH differed from the installation default. The example now
   uses `~/.local/android-publisher-cli/bin`.
2. Public-data scanning omitted commit messages and annotated tag metadata.
   The gate now inspects reachable Git metadata as well as current and historical
   files, with regression tests for privacy and credential patterns.
3. A successful tag followed by interrupted draft creation could strand a
   version. Recovery now verifies the exact tag, draft metadata, and existing
   asset bytes, then uploads only missing assets.
4. Git replacement refs could conceal original historical content. The gate
   rejects replacement refs and forces raw object inspection in every Git and
   Gitleaks subprocess.
5. A real dependency-update commit exposed an overly strict privacy check on the
   official bot's final sign-off. Only the exact official bot identity and trailer
   receive a privacy exception; raw secret-scanner input stays unchanged. Source,
   tag/ref text, other identities and neighboring private text remain blocked.

All five findings were corrected and rechecked by the original independent
reviewers, with no remaining P0–P2 findings in their assigned scopes. Eighteen
privacy regression tests, thirteen draft-recovery fault tests, workflow lint,
the complete source/history/metadata scan and final package smoke checks passed.
Later hosted acceptance evidence is recorded through a normal pull request.

## Cloud and release acceptance

Hosted acceptance uses the standard Apple Silicon runner, including the complete
suite and two-version installation/recovery checks. Normal checks upload no
release artifact. The clean public repository records its hosted results in
[CI history](https://github.com/wangyiliangnet/android-publisher-cli/actions/workflows/check.yml).

An independent read-only remote review confirmed that both versioned rulesets
are active without bypass actors and actually apply to `main` and `v*` tags.
Only squash merging is enabled. Actions require full commit pins and GitHub-owned
actions, default to read-only permissions, and cannot approve pull requests.
Private vulnerability reporting, secret scanning, and push protection are on.

The acceptance pull request exercises the required-check merge gate. Its checks
and merge event on GitHub are the live result. Each preview release run records
its current-main validation, full check, package acceptance, and draft writer
as separate jobs in [Actions](https://github.com/wangyiliangnet/android-publisher-cli/actions/workflows/release.yml).
The first preview remains an ad-hoc signed, unnotarized draft; fixed-identity
distribution is a separate capability.

## Downloaded artifact review

The first draft's actual hosted artifact passed checksum, signature, architecture,
clean source-commit, manifest, license, and installation/recovery checks on a Mac.
A final independent review found a P3 offline link defect in the packaged
changelog. The builder now rewrites that link, and package acceptance checks all
root Markdown documents. The expanded check rejects the original archive. This
correction uses a new `0.1.0-beta.2` candidate without replacing the earlier tag
or package bytes.

## Clean public bootstrap

The public repository starts from one reviewed source snapshot with noreply
metadata. Contributor web-identity privacy is a prerequisite before opening a
pull request. Cloud checks and a draft preview are repeated against this clean
public baseline. Earlier evaluation artifacts are retained privately; no earlier
Git objects are imported into this repository.

Independent review confirmed that the fresh public baseline has one noreply root
commit, no earlier Git objects, and only documentation differences from the
reviewed source. Anonymous checks cannot resolve the earlier test-merge object
through either the public repository or its private archive. Contributor account
privacy was checked before opening the clean repository's acceptance PR.
