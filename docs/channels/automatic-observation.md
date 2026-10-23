# Automatic observation

Run `release batch status --batch BATCH_ID --refresh` to collect available information in one command. Routine use does not require a browser login or manual status entry. A specific unresolved account, protocol, or version-association problem may still need a vendor-console check.

The CLI keeps verified target review/availability separate from `channel_context`: current channel facts, explicitly labeled inference, and information that cannot be associated with the frozen release. Context never resolves an unknown write and never increases verified approved/live counts.

## Xiaomi

The CLI automatically queries the package details endpoint for the account's returned version name/code. Xiaomi documents that endpoint as a package query, not a version-bound review-result or store-availability query. [Official API guide](https://dev.mi.com/xiaomihyperos/documentation/detail?pId=1134).

Only after a matching local submission receipt exists does the CLI show this comparison as an **inference**:

| Returned API version code | Displayed inference | Meaning |
| --- | --- | --- |
| Lower than the frozen target | In review — inferred | The queried version has not caught up to the submitted target |
| Equal to the frozen target | Approved — inferred | The API now reports the target version; review and public availability are still not verified |
| Higher than the frozen target | Unknown | The query may describe a later version |
| No confirmed local submission | Unknown | A comparison cannot establish submission or review |

The output retains the exact inference basis and `association: unconfirmed`. A failed query does not produce a fresh inference. Xiaomi target `review_state` and `availability_state` remain unknown where the API cannot prove them.

## Tencent MyApp (应用宝)

The CLI queries the developer audit endpoint for the current audit slot and reads the package's official public detail page for its displayed store version. Returned audit statuses include in review, rejected, approved, or withdrawn; an unknown code stays unknown. A rejected result may include a bounded, sanitized explanation. API reference: [MyApp developer guide](https://wiki.open.qq.com/index.html#/guide/develop?id=4015262492); public source: [MyApp store](https://sj.qq.com/).

These observations carry **“not confirmed to belong to this version”**. An unversioned audit slot may refer to another submission. A public store version is useful package-level information but does not automatically bind the audit result to this frozen release. Even matching version text is not sufficient to relax the target's verified state or write barriers.

The public-page request uses no credential, cookie, or browser session. It uses HTTPS, an exact allowed host and package path, bounded time/body size, no redirects, and structured data that must match the requested package. An unexpected page shape fails closed rather than extracting an arbitrary version number from nearby text.

## Partial failures and cached data

API and public-store health are independent. An API failure can coexist with a successful public version query; a page failure can coexist with a useful audit result. The aggregate query result then reports partial health. Public information does not conceal an authentication failure.

Retained facts have their original collection time and an explicit cached label. They are distinct from the latest query's source health. Cached context does not become fresh because a later query failed. Disputed verified evidence remains disputed after context-only queries.

Reports preserve these distinctions. Treat inference and unassociated facts as operational context, and seek console evidence only for the concrete question the automatic sources cannot answer.

## Implementation boundary

The API-first discovery approach was informed by the general workflow of [apkgo](https://github.com/KevinGong2013/apkgo). Protocols and parsers here are independently implemented. apkgo's source has separate license terms; this project's MIT license does not relicense that source or vendor documentation. See [protocol evidence](protocol-evidence.md).
