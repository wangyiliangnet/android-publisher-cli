# 查询结果与处理中断

提交完成后，你需要分别确认商店是否受理、是否审核通过、是否已经上架。命令中断时，也先查看同一批次的进度，再决定是否继续。

本页继续使用发布批次 `demo-1-0-0`。批次的创建过程见[发布应用更新](release-workflow.md)。

## 查询最新结果

```sh
android-release batch status --batch demo-1-0-0 --refresh
```

`--refresh` 会提示输入渠道密钥，向商店查询并保存结果。去掉 `--refresh` 则只查看上次保存的记录，可在离线时使用。需要只查部分渠道时，刷新命令可加 `--channels huawei,honor`。

先看各渠道的 `targets`，再看批次汇总。下面这些字段各自回答一个问题：

| 你要知道什么 | 字段与取值 |
| --- | --- |
| 提交走到哪一步 | `execution_phase`：`prepared` 已保存批次，`preflighted` 已通过预检，`submitted` 商店已受理，`unknown` 执行结果待核对 |
| 审核是否通过 | `review_state`：`in_review` 审核中，`approved` 通过，`rejected` 拒绝，`unknown` 尚无法确认 |
| 这个版本是否已上架 | `availability_state`：`live` 已上架，`not_live` 未上架，`unknown` 尚无法确认 |
| 最近一次查询是否成功 | `query_status`：`never` 尚未查询，`ok` 查询成功，`failed` 查询失败；`last_success_at` 是最近成功时间 |
| 是否有操作不能直接重试 | `recovery_required: true` 表示有未决令牌申请或发布请求；同范围其他批次的未决写入也会阻塞当前批次 |
| 为什么阻塞、下一步做什么 | `recovery` 列出阻塞类型、时间、操作步骤、原批次与 `next_action` |
| 最近一次令牌申请怎样结束 | 荣耀、OPPO 的 `account_token_status`：`confirmed_applied` 已取得令牌，`confirmed_rejected` 已被拒绝，`confirmed_not_applied` 未发送，`intent` 或 `unknown` 结果未确定 |
| 查询接口能否确认本次状态 | `query_support.review` 与 `query_support.availability` 分别表示当前实现的审核与上架查询能力 |

`accepted_targets`、`approved_targets` 和 `live_targets` 分别统计受理、审核通过和上架的渠道数。`total_targets` 是整个批次的渠道数，只提交部分渠道时，汇总不会自动缩小到所选范围。

批次级 `recovery_required` 汇总全部渠道的阻塞情况。`query_support` 为 `false` 时，需要到后台核对该项结果。它与查询是否成功分别记录，`query_status: ok` 不代表工具能确认审核或上架。

查询失败时先看 `last_error`，检查网络、账号密钥和权限后再次刷新。已有审核与上架记录会保留，`last_success_at` 也保留原值，因此要同时看查询时间和 `query_status`。查询失败不代表审核被拒绝。

## 为什么有些状态一直无法确认

CLI 必须确认商店返回的结果属于本次版本，才会更新审核或上架状态。商店返回的其他版本或整个账号的信息，会放在 `channel_context` 中，标为 `unconfirmed`，只供参考。

| 渠道 | 当前可确认的范围 |
| --- | --- |
| 华为 | 已有本次受理记录且版本匹配时，分别读取审核和在架版本 |
| 荣耀 | 审核结果匹配本次 `releaseId` 时，确认审核状态；当前查询不确认上架 |
| OPPO | 版本、已上传 APK 的 MD5 和版本标识匹配后，分别确认审核与上架 |
| vivo | 返回的版本匹配时，分别确认审核与上架 |
| 小米 | 仅有账号下的包查询，不能确认本次审核或公开上架 |
| 应用宝 | 审核查询缺少本次版本标识，不能确认本次审核或公开上架 |

遇到这些限制，请在对应商店后台核对目标版本。`unknown` 表示工具没有足够信息，不表示失败。后台核对结果也不会自动改变 CLI 的本机记录。

## 报错或中断后继续

先查看已有记录。这一步离线运行，不需要密钥：

```sh
android-release batch status --batch demo-1-0-0
```

按各渠道的 `recovery` 判断下一步：

| 情况 | 处理方法 |
| --- | --- |
| `account_token_status: confirmed_rejected` 或 `confirmed_not_applied`，且没有其他阻塞 | 核对 API 账号标识和密钥，修正后重试原命令。无需新建批次或删除状态 |
| `recovery` 中有 `kind: account_token` | 令牌申请结果未确定，继续阻止申请。在后台核对 API 账号，并联系维护者检查本机令牌记录；`reconcile` 会报告阻塞，不重复申请令牌 |
| `recovery` 中有 `kind: business_write` | 查看 `batch_id`、`step` 和 `operation_id`，执行 `next_action` 中指向原批次的核对命令；阻塞可能来自另一个批次 |

例如，核对华为的原批次：

```sh
android-release batch reconcile --batch demo-1-0-0 --channels huawei
```

`reconcile` 只读取远端发布数据。需要查询商店时会提示输入渠道密钥。请求发出后丢失响应，商店可能已经执行。当前六个渠道尚不能凭现有接口自动确定这类写入的结果，因此核对后仍可能保留阻塞。

出现 `recovery_required: true` 时，请在渠道后台核对具体版本与操作，并交给维护者检查。保留原提交计划和本机记录。不要重新建批次、更换账号备注或删除状态库来绕过阻塞，这可能造成重复提交。

如果没有未确定结果的请求，只是后续步骤尚未完成，可用原提交计划继续：

```sh
android-release batch resume --batch demo-1-0-0 \
  --approval "/absolute/path/approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

`resume` 会复核原计划与 APK，跳过已确认完成的步骤；遇到结果未确定的步骤会停止。将示例校验值替换为原计划的 `approval_plan`。

若提示 `remote baseline changed after review`，说明核对计划后商店资料发生了变化。在没有未确定请求的情况下，重新预检、生成新计划文件并核对，再决定是否继续。

退出码 `0` 表示命令完成；`2` 表示参数、文件或环境等错误；`3` 表示部分渠道失败或结果未确定。渠道错误详情见输出中的 `errors`。`reconcile` 对所选渠道仍未解决的阻塞返回 `3`；其他渠道可能仍需处理 `recovery_required`。退出码不表示审核通过或已经上架。

## 导出记录

```sh
android-release batch report --batch demo-1-0-0 \
  --file "/absolute/path/report.md"
android-release history
```

`report` 把当前保存的批次状态和阻塞诊断导出为 Markdown，不主动刷新商店结果。诊断包含操作编号、时间和下一步，不含令牌或上传授权。文件的父目录须已存在，文件名须尚未使用。`history` 列出本机保存的全部发布批次，供你找回批次名。

报告不含密钥、令牌或带签名的上传地址，但含包名、版本和发布进度，分享前请检查。

## 本机数据与备份

配置、APK 和图标副本、发布记录、短期令牌与上传授权保存在：

```text
~/Library/Application Support/AndroidPublisherCLI/v2/
```

其中 `projects/` 保存应用配置，`artifacts/` 保存发布文件副本，其余发布状态由 SQLite 数据库管理。账号密钥文件仍由你保管，CLI 不使用 macOS 钥匙串。

CLI 没有备份或恢复命令。需要备份时，先停止所有 CLI 进程，再复制整个 `v2` 目录，包括数据库附属文件。备份含敏感资料，请私下保存。恢复后，先核对商店结果，再决定是否继续发布；不要只复制单个数据库文件。
