# Daily CLI workflow

Use one local project profile for a package, approved signer, and fixed channel set. Use one new immutable batch ID for each distinct publishing intent. All examples below use synthetic identifiers and absolute input paths.

## Configure once

```sh
release setup --project demo --apk "/absolute/path/trusted.apk"
```

The wizard verifies the selected signed APK and asks you to approve its package and signer. It then saves only public configuration and Keychain reference names. It does not read credentials or call a channel. Cancellation and EOF do not save a profile; replacing a profile requires `--replace` and final confirmation.

Import credentials separately through `release credentials import`. The hidden terminal prompt keeps secret values out of arguments and shell history. `--file` imports exact bytes from an owner-private regular file, appropriate for a Huawei service-account JSON or Xiaomi public certificate. `--replace` explicitly replaces an existing entry. Never use an issue or a pull request as a credential transport.

## Prepare, preflight, and review

```sh
release prepare --project demo --batch demo-1-0-0 \
  --notes "/absolute/path/release-notes.txt" \
  --apk oppo="/absolute/path/oppo.apk" \
  --apk honor="/absolute/path/honor.apk"
release batch preflight --batch demo-1-0-0
release batch review --batch demo-1-0-0 \
  --file "/absolute/path/new-approval.json"
```

This example assumes a profile containing exactly OPPO and HONOR. Provide an explicit APK for every configured channel. Preparation checks the package, approved signer, version name/code, and file hashes before freezing. It does not search for an APK or choose the newest file for you.

`prepare` and `plan` are offline. `preflight` reads vendor state and may coordinate token lifecycle, but cannot upload or submit the app. `batch review` is a local check; it requires saved preflights for the selected channels and creates a new approval file without replacing an old one.

Review the package, public account/application identifiers, version, APK and signer hashes, notes, notes changes where available, selected channels, and `after_approval` publication policy. The returned `approval_plan` covers these inputs and their frozen bindings. Approval files contain publishing details and should remain private.

The file-based alternative is `release init --file "/absolute/path/new-input.json"`, followed by `release prepare --file "/absolute/path/input.json"`. Edit its placeholders with public identifiers and Keychain references only. Do not combine `--file` with project preparation flags.

## Explicitly submit

```sh
release batch submit --batch demo-1-0-0 \
  --approval "/absolute/path/new-approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

This command performs real uploads and submission. The digest must exactly match the reviewed file and current frozen inputs. All selected inputs are checked before the first channel dispatch. Changed APKs, configuration, notes, preflights, or channel selection require a new review as applicable.

To publish a subset, include the same `--channels oppo,honor` selection in both review and submit/resume. The approval file binds its selection. Results still include the complete batch scope; success for a subset is not success for the whole batch.

## Query and report

```sh
release batch status --batch demo-1-0-0 --refresh
release batch watch --batch demo-1-0-0 --format table
release history --package com.example.app --limit 20
release report --batch demo-1-0-0 \
  --file "/absolute/path/new-report.md"
release report --release demo-1-0-0-oppo \
  --format json --file "/absolute/path/new-report.json"
```

Without `--refresh`, batch status uses local cached state. `watch` periodically performs read-only queries and respects each target's durable next-check time. Ctrl-C stops watching; it does not cancel vendor review.

Execution, review, availability, and query health are separate columns. `accepted` means the store accepted a submission, not that the version passed review or became live. Automatic context and inference have separate labels and collection times. See [automatic observation](channels/automatic-observation.md) for Xiaomi and MyApp boundaries.

`history` and `report` read local cache only. Reports cover the complete selected release or saved batch, including operation summaries, evidence sources, timestamps, and hashes. They omit credentials, credential references, private account configuration, remote operation references, and raw responses. Application information, notes, and rejection explanations can still be sensitive; review before sharing. Report destinations must be new absolute files in an existing parent directory without symlinks.

## Recover safely

```sh
release batch reconcile --batch demo-1-0-0
release batch resume --batch demo-1-0-0 \
  --approval "/absolute/path/new-approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

Reconciliation is read-only. Resume skips accepted targets and preserves the exact approval gate. Unknown writes block the conflicting target rather than being sent again. Vendor-specific authorization replacement or renewal is an exceptional single-channel workflow and is not silently applied to a batch.

A network error or killed process does not prove the remote side did nothing. If the CLI cannot establish the outcome from verified evidence, retain the records and investigate the specific operation. Do not edit SQLite, delete an operation, create a new release to bypass a block, or treat Xiaomi/MyApp context as reconciliation proof.

## Local files and backup

- Runtime: `~/Library/Application Support/AndroidPublisherCLI/`
- Simulation: `~/Library/Application Support/AndroidPublisherCLI/Simulation/`
- Logs: `~/Library/Logs/AndroidPublisherCLI/`
- Keychain service prefix: `io.github.wangyiliangnet.android-publisher-cli.v1`

```sh
release state backup --to "/absolute/path/new-backup-directory"
release state recovery-status
```

A backup is sensitive and is not a complete machine migration. Credentials, all reusable configuration/input files, and external account permissions require separate handling. Restoring with `release state restore --from "/absolute/path/backup-directory"` activates a recovery barrier and requires inventory reconciliation before writes. Restore is not a routine solution to a failed submission.

## Output conventions

Most commands emit structured JSON to stdout; setup and batch review put human prompts or previews on stderr. Status/history tables are intended for people; use `--format json` where supported for scripts. Check the process exit status and `effect`/`next_action`, not just a success-looking line. Code `130` indicates cancellation. Authentication, configuration, local-state, and unresolved-operation failures need different follow-up; use the returned safe instruction rather than retrying all errors alike.
