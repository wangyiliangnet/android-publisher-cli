# Android 发布工具

`android-release` 是运行在 Apple Silicon Mac 上的命令行工具。它为华为、荣耀、小米、OPPO、vivo 和应用宝提交已有应用的 Android 版本更新。发布策略固定为审核通过后立即发布。

命令会先冻结 APK 和发布批次，再预检账号与应用。提交前，你需要审阅批准文件，并明确授权应用、产物和渠道范围。渠道受理提交不表示审核通过或公开上架。

## 安装

安装 [`.node-version`](.node-version) 指定的 Node.js，以及 Android SDK Build Tools 和 JDK。然后安装 npm 包：

```sh
npm install --global android-publisher-cli
android-release version --json
android-release doctor
```

`version` 应显示已安装的版本。`doctor` 的 `ready` 为 `true` 时，表示 CLI 找到了 APK 检查工具。工具缺失时，按[安装与配置](docs/getting-started.md)检查 SDK 和 JDK 路径。

## 文档

| 你要做的事 | 阅读 |
| --- | --- |
| 安装 CLI、配置应用、输入凭据 | [安装与配置](docs/getting-started.md) |
| 准备批次、预检、审阅并提交 | [发布应用更新](docs/release-workflow.md) |
| 查询审核与上架状态、处理中断 | [状态查询与恢复](docs/status-and-recovery.md) |
| 修改代码、测试或提交 PR | [贡献指南](CONTRIBUTING.md) |
| 理解执行约束和渠道接口 | [架构说明](docs/architecture.md)、[渠道协议](docs/channels/protocol-evidence.md) |
| 检查 npm 包或发布 CLI 版本 | [npm 包验收](docs/npm-package.md)、[CLI 版本发布](docs/npm-release.md) |

## 数据与验证范围

CLI 默认在终端读取渠道凭据。项目、冻结产物、操作日志、短期令牌和上传授权保存在 `~/Library/Application Support/AndroidPublisherCLI/v2/`。自动化凭据文件必须属于当前用户，权限为 `0600`。

六个渠道通过了合成响应测试，真实账号仍需逐一验证。运行测试或安装 npm 包不授予渠道提审权限。真实提交前，先核对应用、APK 和渠道的授权范围。

## 许可证

原创代码采用 [MIT 许可证](LICENSE)。依赖和协议来源见[第三方声明](THIRD_PARTY_NOTICES.md)。本项目独立于各渠道厂商。
