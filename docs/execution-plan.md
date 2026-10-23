# Public roadmap and acceptance

The public beta targets Apple Silicon Macs and a local API-first workflow for six Android stores. Production store credentials and application artifacts stay on the operator's Mac.

## Initial beta scope

| Area | Implementation and acceptance scope |
| --- | --- |
| Local core | Offline frozen plans, APK signature inspection, durable intents/receipts, SQLite recovery barriers |
| Credentials and HTTP | macOS Keychain references, account coordination, controlled origins, bounded uploads and responses |
| Six protocols | Independently implemented vendor request/response contracts with synthetic protocol tests |
| Daily workflow | Reusable setup, explicit APK/notes preparation, reviewed batch approval, submit/reconcile/resume |
| Observations | Separate execution/review/availability/health; labeled Xiaomi inference and unassociated MyApp facts |
| History and reports | Offline history; private Markdown/JSON exports with source, time, context, and full-scope counts |
| Mac delivery | arm64 packaging; install/upgrade/rollback/uninstall and tamper rejection acceptance |
| Repository workflow | Public-data hygiene, MIT license, PR-based main, required CI, and reviewed draft releases |

The acceptance suite uses synthetic inputs. Nothing in this table certifies a real vendor account or application. The current capability catalog is available offline with `release protocols`; detailed boundaries are in [protocol evidence](channels/protocol-evidence.md).

See the [public beta acceptance record](public-beta-acceptance.md) for local,
independent-review, hosted-CI, and release evidence.

## Required acceptance for changes

1. Run focused tests for the changed behavior and the complete `PUBLISHER_REQUIRE_ANDROID_TESTS=1 make check` with pinned runtimes.
2. For write/recovery changes, inject lost responses and terminate a real subprocess; verify no duplicate business write after restart.
3. For observation changes, exercise missing fields, old versions, unknown codes, partial failures, and stale cached evidence. Verify the approved/live counts and write barriers remain unchanged by context-only facts.
4. For delivery changes, inspect the archive allowlist and run installation, upgrade, rollback, uninstall, tampering, and symlink-rejection checks.
5. Independently review credential, transport, publishing, recovery, public-data, and workflow changes. Resolve material findings before merge/release.
6. Require CI on the exact merge candidate and review the draft release's provenance, checksums, distribution status, and notes before publication.

## Remaining work

- Validate each operator's account permissions and application binding locally, starting with read-only commands and a test application.
- Expand verified read-only recovery evidence where vendor protocols support it. Keep undocumented lost-response paths blocked.
- Add more synthetic compatibility cases when vendor documents or response shapes change.
- Provide Developer ID signing and Apple notarization as a separately configured release capability; CI preview packages do not supply it.
- Add a second human maintainer and require a review approval when that collaboration model is available.

Browser login and manually entered status are exceptional evidence paths, not routine requirements. New production automation, scheduling, first-time listings, scheduled release policies, and broader platform support need separate scope and acceptance.
