# 日常发布、查询与恢复

本页以项目 `demo` 一次更新六个渠道为例。先完成[首次配置](colleague-quickstart.md)。准备各渠道 APK 和更新说明后，再建立本次批次。所有示例路径都要换成本机绝对路径。

## 准备批次

把本次更新说明写入私有文本文件。再创建 `batch.json`，逐个指定 APK；CLI 不搜索目录或猜测“最新”文件。

```json
{
  "schema_version": 2,
  "batch_id": "demo-1-0-0",
  "project_id": "demo",
  "release_notes_file": "/absolute/path/release-notes.txt",
  "apks": {
    "huawei": "/absolute/path/huawei.apk",
    "honor": "/absolute/path/honor.apk",
    "xiaomi": "/absolute/path/xiaomi.apk",
    "oppo": "/absolute/path/oppo.apk",
    "vivo": "/absolute/path/vivo.apk",
    "tencent": "/absolute/path/tencent.apk"
  }
}
```

```sh
release prepare --file "/absolute/path/batch.json"
```

命令会验签每份 APK，核对项目包名、签名及一致的版本。随后把产物复制到本机私有目录，并保存不可变批次。输出的 `batch_id` 应是 `demo-1-0-0`，`total_targets` 应为 `6`。失败时修正输入，再重新运行。换 APK 或说明时，使用新的批次名。

`prepare` 不访问渠道。下一步才会读取真实账号数据：

```sh
release batch preflight --batch demo-1-0-0
```

CLI 会在终端提示六个渠道的凭据。预检读取账号和应用状态，并保存与批次绑定的结果。它不上传 APK 或提审。查看 `errors`，逐项处理失败的渠道。某个渠道成功不能代替整个批次通过。

## 审阅与提交

预检全部通过后，先建好仅自己可访问的输出目录，再生成批准文件：

```sh
release batch review --batch demo-1-0-0 \
  --file "/absolute/path/approval.json"
```

核对输出中的账号、应用标识、包名、版本、渠道、APK 摘要、签名、说明和 `release_policy`。文件和输出中的 `approval.approval_plan` 是本次计划摘要。批准文件含发布内容，应私下保存。

**下面的命令会上传 APK 并向六个渠道提审。** 确认你已获得覆盖应用、产物和渠道范围的授权。把摘要原样填入命令：

```sh
release batch submit --batch demo-1-0-0 \
  --approval "/absolute/path/approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

提交前，CLI 再次核对批准文件、预检和所有选定 APK。每个业务请求都先记录意图，再保存可验证的回执。`accepted_targets` 统计已受理的渠道，不表示审核通过或公开上架。部分失败时保留全部输出和本地状态。

只获准提交部分渠道时，在 `preflight` 和 `review` 使用相同的 `--channels oppo,honor`。提交命令直接使用批准文件绑定的选择，不再传 `--channels`。

## 查询与报告

```sh
release batch status --batch demo-1-0-0 --refresh
release batch report --batch demo-1-0-0 \
  --file "/absolute/path/report.md"
release history
```

刷新状态会提示输入凭据，并分别记录执行、审核、上架和查询健康。不加 `--refresh` 时，`status` 只读缓存。理解小米和应用宝的未关联信息，请读[自动查询说明](channels/automatic-observation.md)。

报告文件的父目录也须已存在。报告只含本地摘要和已关联状态，不含渠道密钥、令牌或签名 URL。报告仍可能暴露包名、版本和发布进度；分享前请检查。

## 中断与恢复

网络错误或终端退出后，先看缓存，再运行只读核对：

```sh
release batch status --batch demo-1-0-0
release batch reconcile --batch demo-1-0-0
```

响应丢失时，请求可能已经生效。当前协议证据不足以自动确认这些写入；`reconcile` 会保留阻塞。不要删除状态库、修改回执或创建新批次来绕过未决写入。核查远端结果后，交给维护者处理。

若没有未决写入，且只是后续步骤未完成，可继续使用原批准文件：

```sh
release batch resume --batch demo-1-0-0 \
  --approval "/absolute/path/approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

`resume` 跳过已确认步骤，并复核批准内容。退出码 `3` 表示部分目标失败或结果未决。退出码 `2` 表示输入或配置错误。任何提交错误都要先查看 `recovery_required`。

## 本地数据

新版项目、冻结产物、SQLite 日志、令牌和上传授权位于 `~/Library/Application Support/AndroidPublisherCLI/v2/`。0.1.x Go 版数据仍留在原目录，新版不会读取或迁移它。

当前版本没有状态备份或恢复命令。需要备份时，先停止所有 CLI 进程，再私下复制整个 `v2` 目录及 SQLite 附属文件。恢复后先核对远端结果，再决定是否允许继续发布。备份含敏感状态，不能进入仓库或构建产物。
