# Protocol capabilities and evidence

This is an implementation evidence map, not a certification of a developer account. The public repository contains synthetic fixtures and tests, not live account evidence. `release protocols` prints the offline capability catalog; `protocol_mock_verified` must not be read as `account_verified` or `live_verified`.

## Implemented scope

| Channel | Update path | Important boundary |
| --- | --- | --- |
| Huawei AppGallery | Authorize upload, upload APK, bind, update allowed metadata, submit | Asynchronous package processing; upload origin must satisfy the controlled host policy; no general lost-response retry proof |
| HONOR | Authorize upload, upload APK, bind, update allowed metadata, submit | Account-level API credentials; release identity is needed for version-specific audit evidence; undocumented clear/retry behavior stays blocked |
| Xiaomi | Signed package query and push | Required icon/public certificate and structured test-account data where applicable; review is inference only; no verified API review/live result |
| OPPO | Authorize upload, upload APK, submit | One-time upload authorization; exact approved intent and preflight; undocumented authorization lifetime or lost response must not trigger a blind retry |
| vivo | Upload APK, submit | Single-APK update scope; separate 32/64-bit package orchestration is deferred; ambiguous protocol choices need account evidence |
| Tencent MyApp | Authorize upload, upload APK, submit | Main developer account scope; an unversioned audit slot is not target proof; public-page version is independent context |

Production submissions currently support an update with the `after_approval` policy: publish immediately after vendor approval. This is not a universal first-listing or arbitrary metadata-management client. Inspect the CLI's exact preflight and approved plan for the application in use.

Tests cover synthetic success/error payloads, large identifiers, malformed/missing fields, upload grant restrictions, account coordination, durable receipts, and uncertain writes. Tests do not establish vendor access, approval timing, public availability, copyright compliance, or current service permission for an operator.

## Official reference entry points

References were reviewed for the public documentation on 2026-10-23. Vendor pages can move, require login, or render dynamically. A linked entry point is not a claim that every page was retrievable or that every undocumented behavior has been validated.

| Vendor | Primary source | Use and limits |
| --- | --- | --- |
| Huawei | [AppGallery Connect API introduction](https://developer.huawei.com/consumer/en/doc/AppGallery-connect-Guides/agcapi-overview-0000001158245083) | Publishing and upload API families; consult the current method reference before protocol changes |
| HONOR | [API package-upload guide](https://developer.honor.com/cn/doc/guides/101359) | OAuth, application identity, upload and publishing contracts; dynamically rendered pages may require the developer portal |
| Xiaomi | [Automatic publishing API guide](https://dev.mi.com/xiaomihyperos/documentation/detail?pId=1134) | Signed query/push contracts; the query returns package version information rather than an explicit review result |
| OPPO | [Developer service center](https://open.oppomobile.com/) | Locate the current application publishing API documentation in the official portal; do not infer a contract from unrelated API products |
| vivo | [API access guide](https://developer.vivo.com/doc/detail?id=73) | API access and publishing families; use the matching platform/region documentation for an account |
| Tencent MyApp | [Developer API guide](https://wiki.open.qq.com/index.html#/guide/develop?id=4015262492), [public store](https://sj.qq.com/) | Developer audit query and public package context; document rendering and public-page structures may change independently |

For adapters changed in a pull request, record the exact official method reference, fields used, source access limitations, synthetic test cases, and any unresolved account-validation question. Preserve unknown states for missing, unrecognized, or older-version evidence.

## Third-party ideas and licensing

[apkgo](https://github.com/KevinGong2013/apkgo) is a workflow reference for automatic discovery. Its PolyForm Noncommercial license is not the license of this repository. The protocol code here is independently implemented; do not copy or translate apkgo source under this project's MIT license without separate permission. Vendor documentation and service access retain the vendors' terms.

Go, npm, and other dependencies retain their own licenses and notices. Distribution packaging collects applicable Go dependency notices. A dependency update must also review its license and the runtime/package contents it adds.
