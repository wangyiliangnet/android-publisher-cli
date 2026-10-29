# 命令参考

本页用于查参数。首次操作请从[安装](getting-started.md)、[配置](configuration.md)和[发布流程](release-workflow.md)开始，了解每一步的作用。

示例中的 `ID` 是应用配置名或发布批次名，`FILE` 是本机文件的绝对路径，`SHA256` 是提交计划中的 `approval_plan`。命令中的渠道名称为 `huawei`、`honor`、`xiaomi`、`oppo`、`vivo`、`tencent`。

## 安装检查与配置

| 命令 | 用途 |
| --- | --- |
| `android-release version [--json]` | 查看 CLI 版本；`--json` 同时显示 Node.js 版本与平台 |
| `android-release doctor [--json] [--sdk-root DIR] [--java-home DIR]` | 检查本机 Android 工具，失败时显示修复步骤 |
| `android-release inspect --apk FILE [--sdk-root DIR] [--java-home DIR]` | 检查 APK 签名、包名、版本和 CPU 架构 |
| `android-release setup --project ID [--replace]` | 用交互向导保存应用配置；`--replace` 修改已有配置 |
| `android-release prepare --file FILE [--sdk-root DIR] [--java-home DIR]` | 读取本次发布配置，确认 APK 后保存发布批次 |
| `android-release --help` | 查看命令列表 |

`--sdk-root` 与 `--java-home` 只影响本次命令。希望后续命令也使用相同目录时，设置 `ANDROID_SDK_ROOT` 与 `JAVA_HOME`，见[环境检查](getting-started.md#运行检查并修复问题)。

## 发布与查询

以下每条命令都以 `android-release batch` 开头。

| 后续命令 | 用途 |
| --- | --- |
| `preflight --batch ID [--channels LIST]` | 检查商店账号与现有版本，不上传 APK |
| `review --batch ID --file FILE [--channels LIST]` | 离线生成提交计划文件 |
| `submit --batch ID --approval FILE --approve-plan SHA256` | 复核计划，上传 APK 并提交审核 |
| `resume --batch ID --approval FILE --approve-plan SHA256` | 继续原发布，跳过已确认步骤 |
| `status --batch ID` | 查看本机保存的状态 |
| `status --batch ID --refresh [--channels LIST]` | 向商店查询最新状态 |
| `reconcile --batch ID [--channels LIST]` | 只读核对中断请求，无法确认时保留阻塞 |
| `report --batch ID --file FILE` | 将已保存状态导出为 Markdown |

`LIST` 是逗号分隔的渠道名称，例如 `huawei,honor`。生成提交计划时选定渠道，提交与继续执行使用计划里的范围；这两个命令也支持 `--sdk-root DIR` 和 `--java-home DIR`。状态与中断处理见[查询结果与处理中断](status-and-recovery.md)。

`android-release history` 列出已有发布批次。`android-release protocols` 显示各渠道的模拟接口测试与真实账号验证范围，字段含义见[渠道接口与验证范围](channels/protocol-evidence.md)。

## 输出与退出码

`doctor` 默认输出中文诊断文字，`--json` 输出诊断对象。`version` 默认输出版本号，`--json` 输出版本对象。其他命令将 JSON 结果写到标准输出，交互提示写到标准错误。

| 退出码 | 含义 | 下一步 |
| --- | --- | --- |
| `0` | 命令已完成 | 按命令结果进入下一步；提交受理后仍需查询审核和上架 |
| `2` | 参数、配置、文件或环境错误 | 查看 `error`；环境问题运行 `doctor`，修复后重试 |
| `3` | 至少一个渠道失败或结果未确定 | 查看 `errors` 与 `recovery_required`，先核对再继续 |

`reconcile` 完成查询时也可能仍保留未确定结果的请求。判断是否可继续，要看 `recovery_required`，不能只看退出码。
