# Security policy

This is beta software that can perform store-side publishing writes. Use the latest beta and validate it with a non-production application before adopting it for a production account. No long-term support branch or guaranteed response deadline is currently offered.

## Report a vulnerability privately

Use GitHub's **Security → Report a vulnerability** for this repository when available. If the private reporting form is unavailable, open an issue asking for a private contact method and include no exploit details, account identifiers, or sensitive files.

Describe the affected version or commit, a minimal synthetic reproduction, impact, and proposed mitigation. Do not attach API secrets, private keys, certificates with private keys, cookies, tokens, signed URLs, real APKs, raw vendor responses, or a publishing database. Review diagnostic output before sharing it.

## Security boundaries

- Production credentials are resolved locally through macOS Keychain references. Never store them in repository files or GitHub Actions secrets for this project's CI.
- Publishing state, baselines, artifacts, and evidence remain in private local storage. Reports omit credential values and raw responses but can still contain application names, notes, versions, and identifiers; review them before sharing.
- Production writes require an explicitly approved immutable intent and preflight. An unresolved write remains blocked until reconciliation or verified protocol idempotency resolves it.
- Automatic or public-store context is useful information, not proof that a particular release was reviewed or is live.
- CI uses synthetic data and temporary test identities. A green build does not prove store permissions, Apple notarization, or production readiness.

If a credential may have leaked, revoke or rotate it at the vendor first, then replace the local Keychain entry. Removing a Git file alone does not revoke a secret or remove its history.
