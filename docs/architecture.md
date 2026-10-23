# Architecture

The CLI is a local executor with a durable journal. It separates a user's approved publishing intent from vendor protocols, credentials, and observations.

| Area | Responsibility |
| --- | --- |
| `internal/cli` | Setup, immutable batch preparation, approval files, commands, history, and reports |
| `internal/artifact` and `internal/manifest` | APK inspection, hashes, signer checks, and frozen release snapshots |
| `internal/engine` and `internal/state` | Dispatch, SQLite operation journal, evidence projection, and recovery barriers |
| `internal/channels` and `internal/protocol` | Typed vendor operations, validation, capability boundaries, and observations |
| `internal/account`, `internal/credentials`, `internal/transport` | Account coordination, Keychain references, controlled HTTP, and upload grants |
| `internal/simulation` and `internal/browser` | Loopback stores, synthetic browser fixtures, and failure acceptance tests |

## Publishing path

`setup` saves a reusable local profile containing the package, approved signer, public account identifiers, and Keychain reference names. `prepare` inspects explicit APK inputs, checks all channels agree on package/version/signer, freezes artifacts and notes, and creates an immutable batch. Neither command contacts a vendor.

`preflight` reads the account/application baseline and binds it to the frozen intent. `batch review` verifies the saved inputs and creates a new immutable approval file. Its digest binds the selected channels, APKs, notes, policy, configuration, and saved preflight. `batch submit` validates the reviewed plan before dispatching any selected target.

Each business operation is journaled before its request. Only a verified response can produce an applied receipt. A lost response produces uncertainty, not permission to retry. `reconcile` is read-only; `resume` keeps the original approval and unresolved-write checks. A prior unresolved release also blocks conflicting writes for the same channel, account, and application.

## States and evidence

Execution, review, availability, and query health are independent. A successful submission means accepted by the endpoint; it does not mean review approval or store availability. Unknown fields and old-version evidence cannot silently establish target success. Source, collection time, and disputes accompany observed facts.

Xiaomi version comparison and MyApp's unversioned audit slot live in a separate `channel_context`. They never resolve an unknown write or increase verified approved/live counts. Current source health and retained cached context are distinct, so an API failure does not erase useful cached data or make it appear fresh.

## Local storage and concurrency

Production data uses `~/Library/Application Support/AndroidPublisherCLI/`, with simulation state in its `Simulation/` child. Logs use `~/Library/Logs/AndroidPublisherCLI/`. Keychain service names begin with `io.github.wangyiliangnet.android-publisher-cli.v1`.

State uses SQLite WAL with full synchronization and one active local writer. Account lifecycle and application ownership prevent conflicting work. Private-file helpers reject unsafe paths and permissions. Do not move or edit a database while a command is active.

Backups contain journal state and associated frozen evidence/artifacts covered by the backup implementation. They are not a complete machine migration: Keychain items, all external input files, and developer account permissions need separate handling. Restore enters a recovery barrier; it does not authorize new writes.

## Acceptance boundary

Tests use synthetic accounts, credentials, APKs, responses, local HTTP servers, and browser pages. Real subprocess termination exercises journal recovery. The public test result means the implementation meets those contracts; vendor permissions, undocumented behavior, and live account state remain outside that claim.
