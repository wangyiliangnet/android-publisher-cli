# Project instructions

## Scope and evidence

This repository contains the public macOS Android publishing CLI. Read README.md
for commands and docs/architecture.md for the execution model. Before changing
channel protocols, read docs/channels/protocol-evidence.md; distinguish an
implemented protocol, synthetic test evidence and live account verification.
Use generic examples and synthetic fixtures in all tracked files and reports.

## Publishing invariants

- Freeze intent and artifact before business writes. Persist each operation
  intent before its request and its verified receipt immediately afterward.
- Keep execution, review, availability and query health separate. Unassociated
  facts and inferences do not confirm target state or resolve disputed evidence.
- Keep plan offline. Preflight, observations and reconciliation read publishing
  data; route token lifecycle effects through account coordination.
- An unresolved write blocks conflicting writes for the same channel, account
  and application. Resume only after reconciliation or verified idempotency.
- Real store changes require authorization covering application, artifact and
  publishing scope. Initialization, tests and CI never authorize a submission.

## Credentials and runtime state

Resolve secrets through local Keychain references. Keep real account IDs,
credentials, signing keys, APKs, cookies, signed URLs and sensitive responses out
of Git, issues, agent prompts, build artifacts and normal logs. Runtime state is
under ~/Library/Application Support/AndroidPublisherCLI/; installation files are
under ~/.local/android-publisher-cli/. CI uses temporary synthetic credentials and
independent browser profiles, without store accounts or persistent user data.
Implement vendor protocols independently and retain official source references;
resolve third-party licensing before reusing code.

## Validation and contributions

Run make check for Go changes and focused tests for changed behavior. Set both
GO and GOFMT when using a toolchain outside PATH. Recovery changes need fault
injection and real subprocess termination coverage. Packaging changes need
installation, upgrade, rollback, checksum and path-safety smoke checks.

Read CONTRIBUTING.md before opening a PR and docs/release-process.md before
changing workflows, tags or releases. Use codex/<topic> branches, PRs into main
and the required CI check; never bypass a failed check. Keep signing and channel
submission separate from CI preview packaging. When subagents are authorized,
give bounded paths/questions; reviewers are read-only, and the coordinator
integrates fixes and records unresolved findings.
