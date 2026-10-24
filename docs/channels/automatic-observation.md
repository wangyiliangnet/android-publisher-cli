# 自动查询说明

提交后运行以下命令查询当前批次：

```sh
android-release batch status --batch demo-1-0-0 --refresh
```

CLI 会在终端提示输入所需凭据。去掉 `--refresh` 时，只读取本地缓存。查看结果时，先看进程退出码，再看 `targets` 中每个渠道的字段。

| 字段 | 含义 |
| --- | --- |
| `execution_phase` | `submitted` 只表示渠道已受理本次提交 |
| `review_state` | 版本关联证据支持的审核状态 |
| `availability_state` | 版本关联证据支持的公开上架状态 |
| `query_status`、`last_success_at` | 上次查询的健康状态和成功时间 |
| `recovery_required` | 存在未决业务写入，不能直接重发 |

审核通过不等于公开上架。查询失败时，CLI 保留已有成功时间，并把 `query_status` 记为 `failed`。缓存不能看作刚取得的结果。

## 各渠道如何关联结果

华为在本地已有受理回执，并且 API 返回目标版本时，才读取该版本的审核和在架字段。荣耀需要本次提交回执中的 `releaseId`，并核对审核查询返回的相同标识。

OPPO 要求目标版本、已上传 APK 的 MD5 和版本标识一致。vivo 要求详情接口返回目标版本。两个渠道的审核与上架字段分别记录。

小米包查询只给出账号下的版本。目标版本出现在查询结果里，仍不能证明本次审核或公开上架。应用宝审核接口也没有本次版本标识。这两类结果写入 `channel_context`，并标为 `unconfirmed`。它们不会增加 `approved_targets` 或 `live_targets`。

## 未决写入

网络中断或进程结束后，先读取本地状态，再运行：

```sh
android-release batch reconcile --batch demo-1-0-0
```

`reconcile` 只做读取。当前适配器没有足够的响应丢失核对依据，因此未决操作会继续阻塞。`resume` 不会重发未决步骤。请在渠道后台核实具体操作，再交给维护者处理；不要删除 SQLite 记录来绕过阻塞。
