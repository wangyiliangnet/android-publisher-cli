# 发布应用更新

每次发版都建立一个发布批次，保存这次的 APK、更新说明、渠道范围和执行进度。这样你在提交前能核对具体内容，提交后能按同一个批次追踪结果。

先完成[环境检查](getting-started.md)和[应用配置](configuration.md)。本页示例使用应用配置 `demo`，向华为提交一次更新。命令中的绝对路径要替换为本机文件路径。

## 准备 APK 和更新说明

准备已签名、不可调试的正式 APK，以及 UTF-8 文本格式的更新说明。更新说明通常为 5～500 个字符；应用配置包含 vivo 时，最多 200 个字符。长度按 Unicode 字符计算，空白和换行计入上限。各渠道 APK 的包名、版本名、版本代码和签名证书须一致；不同渠道的文件内容可以不同。

小米需要应用配置中的本机图标文件可读取。应用宝的 APK 须包含 `armeabi-v7a`、`x86`、`arm64-v8a` 或 `x86_64` 架构。CLI 会在冻结前检查这些本机条件；渠道账号和商店资料仍在预检时检查。

创建 `batch.json`，列出这次要保存的发布内容：

```json
{
  "schema_version": 2,
  "batch_id": "demo-1-0-0",
  "project_id": "demo",
  "release_notes_file": "/absolute/path/release-notes.txt",
  "apks": {
    "huawei": "/absolute/path/huawei.apk"
  }
}
```

| 字段 | 填什么 |
| --- | --- |
| `schema_version` | 固定填 `2`，表示当前配置格式 |
| `batch_id` | 本次发布的名字，后面的命令都用它找回进度；每次更新用新名字 |
| `project_id` | `setup --project` 使用的应用配置名 |
| `release_notes_file` | 更新说明文件的绝对路径 |
| `apks` | 渠道名称与 APK 绝对路径的对应关系 |

`apks` 必须包含应用配置中的全部渠道，每个渠道一个文件。比如配置了华为和荣耀，就同时写入 `huawei` 与 `honor`；同一 APK 用于两个渠道时，可以填写相同路径。渠道名称见[配置说明](configuration.md#创建应用配置)。只提交其中部分渠道时，在后面的预检和计划生成步骤选择范围。

## 检查并保存这次发布

```sh
android-release prepare --file "/absolute/path/batch.json"
```

CLI 先检查更新说明和图标，再验证 APK 签名、架构及各渠道 APK 的应用与版本是否一致。随后显示包名、版本名（`versionName`）和版本代码（`versionCode`）。核对后输入 `yes`，CLI 才会复制 APK、图标和更新说明，保存到本机发布批次中。这一步不连接商店。

成功时输出本次 `batch_id`，`total_targets` 等于配置的渠道数。小米、OPPO、vivo 填写的包名须与 APK 一致。若检查失败，修正输入后重新运行。

批次保存后，修改原 APK 或更新说明不会改变这次发布。需要换包、改说明或改渠道配置时，用新的 `batch_id` 创建批次。已有批次不能覆盖。

## 检查渠道账号和现有版本

提交前先检查账号能否访问目标应用、现有版本是否允许更新：

```sh
android-release batch preflight --batch demo-1-0-0
```

按终端提示输入[账号密钥](configuration.md#账号密钥)。命令读取商店中的账号和应用资料，保存检查结果，不上传 APK 或提交审核。部分渠道会申请访问令牌。

输出中的 `errors` 应为空对象 `{}`。有错误时，先看 `recovery_required` 和渠道的 `recovery`。没有未决请求时，按错误修正账号、应用编号、包名或版本，再次预检。有未决请求时，先按[恢复步骤](status-and-recovery.md#报错或中断后继续)处理。各渠道独立检查，一个渠道成功不代表其他渠道可提交。

只准备提交部分渠道时，加上 `--channels`，例如 `--channels huawei,honor`，并在下一步使用相同选择。选中的渠道必须都通过预检。

## 核对提交计划

生成计划文件，集中核对这次要提交的应用、版本、账号、渠道和更新说明：

```sh
android-release batch review --batch demo-1-0-0 \
  --file "/absolute/path/approval.json"
```

输出文件的父目录须已存在，文件名须尚未使用。该文件在命令中称为 `approval`，即供你核对并确认的提交计划。这一步离线运行。

打开文件，核对以下内容：

- `target_accounts`、`selected_channels`：应用编号、账号和要提交的渠道。
- `package_name`、`version_name`、`version_code`：包名和版本。
- `artifact_sha256`、`signer_sha256`：APK 内容和签名证书的校验值，用于确保提交的是同一份文件。
- `release_notes`：将写入商店的更新说明。
- `release_policy`：固定为 `after_approval`，即审核通过后立即上架。

`approval_plan` 是整份计划的校验值。下一步要将它原样传给提交命令，防止核对后计划被替换。文件含应用发布资料，请保存在自己的目录中。预检或发布内容有变化时，重新生成并核对计划。

## 上传并提交审核

**此步骤会上传 APK 并向真实商店提交审核。** 确认你获准发布的是计划中这个应用、这些 APK 和这些渠道，再执行命令：

```sh
android-release batch submit --batch demo-1-0-0 \
  --approval "/absolute/path/approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

将 `APPROVAL_PLAN_FROM_REVIEW` 替换为上一步文件或输出中的 `approval_plan`。终端会再次提示输入渠道密钥。提交使用计划中已确认的渠道范围，不再接受 `--channels`。

CLI 先复核计划与 APK，再逐渠道执行。华为绑定 APK 后至少等待 2 分钟；vivo 的请求之间至少间隔 1 分钟，等待期间请保持命令运行。

查看输出的 `errors` 和 `accepted_targets`。`errors` 为空且所选渠道的 `execution_phase` 都是 `submitted`，表示这些渠道已受理提交。`accepted_targets` 统计整个批次；只选部分渠道时，数量可以小于 `total_targets`。

受理后仍需等待商店审核与上架。继续[查询结果](status-and-recovery.md)。若命令报错或中断，也按同一文档先检查已有进度，避免重复提交。
