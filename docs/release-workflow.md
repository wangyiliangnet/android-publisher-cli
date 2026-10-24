# 发布应用更新

示例项目 `demo` 包含六个渠道。先完成[安装与配置](getting-started.md)，准备各渠道 APK 和更新说明。将示例路径换成本机绝对路径。CLI 只更新渠道中已有的应用。

## 准备批次

把更新说明写入私有文本文件。再创建 `batch.json`，明确指定每个渠道的 APK：

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
android-release prepare --file "/absolute/path/batch.json"
```

`prepare` 验签所有 APK，检查包名、版本和签名是否一致。小米、OPPO 和 vivo 的应用 ID 还须与 APK 包名一致。检查过程不访问渠道。

CLI 会显示包名、版本名（`versionName`）和版本代码（`versionCode`）。核对后输入 `yes`，才会冻结产物并保存批次。命令需要可交互的终端。

成功时，`batch_id` 应为 `demo-1-0-0`，`total_targets` 应为 `6`。输入有误时先修正；更换 APK 或说明时使用新的批次名。确认后若 APK 文件变化，CLI 会拒绝保存批次。

## 预检

```sh
android-release batch preflight --batch demo-1-0-0
```

CLI 会提示所选渠道的凭据。预检读取远端账号和已有应用状态，并将结果绑定到批次。它不上传 APK 或提审。检查输出中的 `errors`，逐项处理失败的渠道；其他渠道成功不能代替失败渠道的预检。

只获准提交部分渠道时，在预检和审阅时使用相同的 `--channels`，例如 `--channels oppo,honor`。批准文件会记录选定范围。

## 审阅计划

预检通过后，先创建仅自己可访问的目录，再生成批准文件：

```sh
android-release batch review --batch demo-1-0-0 \
  --file "/absolute/path/approval.json"
```

核对账号、应用标识、包名、版本、渠道、APK 摘要、签名、更新说明和 `release_policy`。输出的 `approval.approval_plan` 是本次计划摘要。批准文件含发布内容，应私下保存。`review` 离线运行，不上传 APK。

## 提交

**下面的命令会上传 APK 并向选定渠道提审。** 执行前确认授权覆盖应用、产物和渠道。把审阅输出的摘要原样填入：

```sh
android-release batch submit --batch demo-1-0-0 \
  --approval "/absolute/path/approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

CLI 会再次核对批准文件、预检和冻结产物。每个业务请求先保存操作意图，取得可验证回执后保存结果。`accepted_targets` 只统计已受理目标，不表示审核通过或公开上架。部分失败时保留输出和本地状态，按[状态查询与恢复](status-and-recovery.md)处理。

提交命令使用批准文件绑定的渠道范围，不接受新的 `--channels` 选择。
