# 状态查询与恢复

`batch submit` 返回后，先检查进程退出码和 `accepted_targets`，再查询每个渠道。渠道受理、审核通过和公开上架是不同状态。

## 查询状态

```sh
android-release batch status --batch demo-1-0-0 --refresh
```

`--refresh` 会提示输入凭据，读取渠道数据并更新本机状态。去掉 `--refresh` 时，命令只读缓存；缓存不代表刚取得的远端结果。

| 字段 | 含义 |
| --- | --- |
| `execution_phase` | `submitted` 表示渠道已受理提交 |
| `review_state` | 与目标版本关联的审核状态 |
| `availability_state` | 与目标版本关联的公开上架状态 |
| `query_status`、`last_success_at` | 查询健康状态和最近一次成功时间 |
| `recovery_required` | 存在未决业务写入，不能直接重发 |

查询失败时，CLI 保留已有的 `last_success_at`，并把 `query_status` 记为 `failed`。不要把查询失败解释为审核失败，也不要把审核通过解释为公开上架。

## 渠道结果的关联

华为需要本地已有受理回执，且 API 返回目标版本，才读取该版本的审核和在架字段。荣耀使用本次提交回执中的 `releaseId`，并核对审核查询返回的标识。

OPPO 要求目标版本、已上传 APK 的 MD5 和版本标识一致。vivo 要求详情接口返回目标版本。两个渠道分别记录审核与上架字段。

小米包查询只给出账号下的版本。即使目标版本出现在结果里，也不能据此确认本次审核或公开上架。应用宝审核接口缺少本次版本标识。CLI 把这些信息写入 `channel_context`，标为 `unconfirmed`；它们不会增加 `approved_targets` 或 `live_targets`。

## 报告与历史

```sh
android-release batch report --batch demo-1-0-0 \
  --file "/absolute/path/report.md"
android-release history
```

报告的父目录须已存在。报告只含本地摘要和已关联状态，不含渠道密钥、令牌或签名 URL。它仍可能暴露包名、版本和发布进度，分享前请检查。

## 中断后核对

网络错误或终端退出后，先读取缓存，再运行只读核对：

```sh
android-release batch status --batch demo-1-0-0
android-release batch reconcile --batch demo-1-0-0
```

响应丢失时，请求可能已经生效。当前协议证据不足以自动确认这类写入；`reconcile` 会保留阻塞。请在渠道后台核实具体操作，再交给维护者处理。保留 SQLite 记录和原批准文件，不能用新批次绕过同一渠道、账号和应用的未决写入。

如果没有未决写入，且只是后续步骤未完成，可用原批准文件继续：

```sh
android-release batch resume --batch demo-1-0-0 \
  --approval "/absolute/path/approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

`resume` 跳过已确认步骤，并复核批准内容；遇到未决步骤不会重发。退出码 `3` 表示部分目标失败或结果未决，`2` 表示输入或配置错误。提交错误后先检查 `recovery_required`。

## 本机状态与备份

项目、冻结产物、SQLite 日志、令牌和上传授权保存在 `~/Library/Application Support/AndroidPublisherCLI/v2/`。CLI 没有状态备份或恢复命令。

需要备份时，先停止所有 CLI 进程，再私下复制整个 `v2` 目录及 SQLite 附属文件。恢复后先核对远端结果，再决定是否继续发布。备份含敏感状态，不能进入仓库或构建产物。
