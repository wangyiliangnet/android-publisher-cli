# Android 发布工具

`android-release` 帮你把同一个 Android 应用的版本更新提交到华为、荣耀、小米、OPPO、vivo 和应用宝。它在 Apple Silicon Mac 上运行，使用你已打包并签名的 APK，更新商店里已有的应用。

你先保存各商店的应用资料。每次发版时，提供 APK 和更新说明，检查渠道账号，核对提交计划，再执行提交。工具会保留每个渠道的进度，供你查询审核结果和处理中断。发布方式固定为审核通过后立即上架。

## 渠道能力与验证范围

六个渠道目前只通过模拟接口测试，尚未完成逐个真实账号和公开上架验证。正式提交前，先用获准的测试应用验证账号权限和结果。

| 渠道 | 本次审核查询 | 本次上架确认 | 更新说明长度 |
| --- | --- | --- | --- |
| 华为 | 支持 | 支持 | 5～500 个字符 |
| 荣耀 | 支持 | 需后台核对 | 5～500 个字符 |
| 小米 | 需后台核对 | 需后台核对 | 5～500 个字符 |
| OPPO | 支持 | 支持 | 5～500 个字符 |
| vivo | 支持 | 支持 | 5～200 个字符 |
| 应用宝 | 需后台核对 | 需后台核对 | 5～500 个字符 |

“支持”表示当前实现能在本机已有受理记录、远端结果关联本次版本时确认状态，不代表已完成真实账号验证。“渠道已受理”只表示收到提交。六渠道均不能自动核对响应丢失的写入，结果未决时会阻止冲突发布。

安装后运行 `android-release protocols` 查看能力说明，脚本使用 `protocols --json`。关联条件见[查询结果](docs/status-and-recovery.md#为什么有些状态一直无法确认)，中断处理见[恢复步骤](docs/status-and-recovery.md#报错或中断后继续)。

## 从安装到完成一次发布

| 阶段 | 你要完成的事 | 命令与说明 |
| --- | --- | --- |
| 首次使用 | 安装工具，检查 APK 检查环境 | `doctor`，[安装与环境检查](docs/getting-started.md) |
| 配置应用 | 保存要更新的应用、渠道和账号资料，以后复用 | `setup`，[配置应用与渠道账号](docs/configuration.md) |
| 每次发版 | 保存本次 APK 和更新说明，生成一个发布批次 | `prepare`，[发布应用更新](docs/release-workflow.md) |
| 提交前 | 检查账号权限和现有版本，核对要提交的内容 | `batch preflight`、`batch review` |
| 提交审核 | 上传 APK，向选定商店提交更新 | `batch submit` |
| 提交后 | 查看受理、审核与上架结果，处理中断 | `batch status`，[查询结果与处理中断](docs/status-and-recovery.md) |

“应用配置”保存长期复用的资料；“发布批次”保存某一次更新的具体版本和进度。一个应用配置可以建立多个发布批次。一个批次在每个渠道只提交一个 APK。

## 开始使用

准备 Node.js 24.22.0 和 npm 11.19.1，再安装：

```sh
npm install --global android-publisher-cli
android-release version
android-release doctor
```

`doctor` 会列出缺失的 Android 工具、安装位置和修复步骤。环境检查通过后，运行 `android-release setup --project demo`，把 `demo` 换成你给应用配置取的名字。完整步骤见[安装与环境检查](docs/getting-started.md)。

## 按需查阅

| 需要解决的问题 | 文档 |
| --- | --- |
| 查看全部命令、参数和退出码 | [命令参考](docs/commands.md) |
| 反馈故障、处理密钥泄露 | [安全与问题反馈](SECURITY.md) |
| 修改代码、运行测试、提交 PR | [贡献指南](CONTRIBUTING.md) |
| 理解发布记录和恢复逻辑 | [开发者架构说明](docs/architecture.md) |
| 修改商店接口 | [渠道接口与验证范围](docs/channels/protocol-evidence.md) |
| 检查安装包、发布本工具的新版本 | [npm 安装包验收](docs/npm-package.md)、[发布 CLI 版本](docs/npm-release.md) |

配置、APK 副本和发布记录保存在本机。渠道密钥按命令提示输入；文件要求见[账号密钥](docs/configuration.md#账号密钥)。备份和迁移见[本机数据](docs/status-and-recovery.md#本机数据与备份)。

原创代码采用 [MIT 许可证](LICENSE)。依赖和来源见[第三方声明](THIRD_PARTY_NOTICES.md)。本项目独立于各商店厂商。
