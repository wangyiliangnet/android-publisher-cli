# 查询结果与处理中断

```sh
android-release status demo
android-release status demo --cached
android-release history demo
```

默认刷新应用最近一次发布。`--cached` 只读本机记录，适合离线或中断后先查看进度。查看历史版本时，用 `--release` 指定 `history` 列出的记录。

## 分别看提交、审核、上架和查询

| 问题 | JSON 字段 |
| --- | --- |
| 提交到哪一步 | `targets[].execution_phase`：`prepared`、`preflighted`、`submitted` 或 `unknown`。 |
| 审核是否通过 | `review_state`：`unknown`、`in_review`、`approved` 或 `rejected`。 |
| 本次版本是否上架 | `availability_state`：`unknown`、`live` 或 `not_live`。 |
| 查询是否成功 | `query_status`、`last_success_at` 和 `last_error`。 |
| 是否需要核对请求 | `recovery_required` 和 `recovery`。 |
| 能否自动确认 | `query_support.review` 和 `query_support.availability`。 |

已受理表示商店收到本次提交。审核通过和公开上架需要分别取得关联证据。查询失败时保留上次结果，并显示上次成功时间。

汇总中的 `accepted_targets`、`approved_targets` 和 `live_targets` 分别统计本次范围的自动结果。人工记录单列在 `manual_observations`，不会增加自动确认数量。

## 自动确认的范围

华为须有本次受理记录并匹配版本；荣耀审核须匹配 `releaseId`；OPPO 须匹配版本、APK MD5 和版本标识；vivo 须匹配版本。

荣耀上架、小米审核与上架、应用宝审核与上架需要后台核对。缺乏关联的账号级或其他版本信息只作为背景，目标结果继续保留为未确认。

```sh
android-release status demo --record-manual
```

按指引核对目标版本，记录结论、时间、来源和备注。记录追加保存，也可以追加修正；展示时明确标记为人工核对。

## 报错或中断后继续

先用 `status demo --cached` 查看。确定尚未发送或可以安全重试的步骤，通过 `publish demo` 选择继续。已受理渠道和已确认步骤会跳过。

有 `intent` 或 `unknown` 时，请求可能已执行。先保留记录，按恢复提示核对：

```sh
android-release status demo --reconcile
```

核对只读取远端发布数据。当前六渠道没有足够的协议证据，在写入响应丢失后自动解除阻塞。后台人工记录也不能解除缺乏可验证关联证据的未决写入，需要维护者进一步核查。

未决令牌申请同样保持记录。已拒绝或确定未发送的令牌请求可在修复凭据后重试；结果未决的申请继续阻塞。

本地状态保护同一渠道、账号和应用的冲突写入。更改账号备注或删除记录会破坏恢复依据，请保留原记录。

## 本机资料

本机目录为 `~/Library/Application Support/AndroidPublisherCLI/v2/`。其中保存应用配置、冻结 APK 和图标、SQLite 发布记录、加密凭据、检查组件及 Web 私有文件。

凭据以 AES-256-GCM 加密，本机钥匙串保存独立加密密钥。短期令牌和上传授权保存在私有状态。清除凭据使用 `account forget`，操作回执和未决记录仍保留。

Web 中上传的 APK 在冻结后使用同一份产物记录，服务关闭时清理临时上传。应用图标作为长期资料单独保存。

备份前停止 CLI 和 Web，再复制完整目录，包括数据库附属文件。备份含敏感资料。恢复后先核对商店结果，再继续发布。
