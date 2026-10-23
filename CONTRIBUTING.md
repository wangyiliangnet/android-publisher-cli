# Contributing

Start with a small issue describing the behavior and acceptance criteria. Do not include real credentials, APKs, store responses, private account data, or signed URLs. Use `com.example.app`, `example.invalid`, and clearly synthetic identifiers in examples and fixtures.

## Local workflow

Before contributing, choose your GitHub noreply address for this checkout and
enable **Keep my email addresses private** in [GitHub email settings](https://github.com/settings/emails).
GitHub's web commit identity is configured separately from local Git; it also
affects generated PR merge metadata. The public-data gate checks both identities
and does not exempt generated merge commits from privacy checks. See
[GitHub's commit email instructions](https://docs.github.com/en/account-and-profile/how-tos/email-preferences/setting-your-commit-email-address).

1. Branch from current `main` using `feat/short-topic`, `fix/short-topic`, or `docs/short-topic`. Automated changes may use `codex/short-topic`.
2. Read [AGENTS.md](AGENTS.md), [the architecture](docs/architecture.md), and [the roadmap](docs/execution-plan.md) before changing publishing behavior.
3. Install the pinned Go, Node.js, and npm versions. Provide Android SDK Build Tools and a JDK for real signature tests.
4. Run `make browser-install`, then `PUBLISHER_REQUIRE_ANDROID_TESTS=1 make check`. Run focused tests while iterating.
5. Open a pull request against `main`, describing the user-visible change, tests, protocol evidence, and any remaining limitations. Link related issues and update documentation.
6. Resolve review conversations and wait for required CI. Merge with squash so each change has one clear main-branch commit. Delete the merged topic branch.

`main` requires a pull request, passing required checks, and resolved review conversations. Direct pushes, force pushes, and branch deletion are blocked by the repository ruleset. There is initially no mandatory second-human approval because the project has one maintainer; independent review remains expected for publishing, credential, transport, recovery, and release changes. Increase the required approval count when another maintainer is available.

## Publishing and recovery changes

Keep `plan` offline. Read-only preflight, reconciliation, and observation must not perform publishing writes. Treat token management as an account-coordinated lifecycle effect.

Freeze meaning and artifacts before a write; persist intent before dispatch and verify the response before recording success. A timeout, process exit, or lost response does not prove the store rejected a request. Unknown writes block conflicting writes across releases.

Add fault-injection coverage for changed write paths. Claims about crash recovery need a real subprocess termination test, not only a returned-error mock. Keep automatic context, inferred review, and stale or unassociated information out of verified target counts and reconciliation decisions.

Use vendor documentation as protocol evidence. Record exact sources and explain any behavior not documented or not account-verified. Do not copy third-party implementations without checking their license and obtaining permission where required.

## Security and releases

Report vulnerabilities through [SECURITY.md](SECURITY.md). Packaging, tags, and draft/public release gates are described in [the release process](docs/release-process.md). CI never uploads to an app store.

Contributions are accepted under the repository's [MIT License](LICENSE). Preserve applicable dependency and third-party notices.
