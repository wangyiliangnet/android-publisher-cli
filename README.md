# Android 发布工具

这个 CLI 使用 TypeScript 和 npm 构建。在 Apple Silicon Mac 上，它可向华为、荣耀、小米、OPPO、vivo 和应用宝提交已有应用的 Android 版本更新。发布规则固定为“审核通过后立即发布”。

凭据默认在终端交互输入。CLI 不读取 macOS 钥匙串，也不保存用户输入的渠道密钥。发布批次、操作日志、短期令牌和上传授权保存在本机私有目录。真实渠道账号尚未完成新版实现的逐一验证；合成测试通过不代表账号具备发布权限。

## 开始使用

先安装 [`.node-version`](.node-version) 指定的 Node.js，以及 Android SDK Build Tools 和 JDK。安装与项目配置见[首次使用](docs/colleague-quickstart.md)。

```sh
npm ci --ignore-scripts
npm run build
node build/cli.js doctor
```

运行 `release prepare` 后，按[日常发布](docs/daily-cli-workflow.md)完成预检、审阅、提交和查询。**真实提交会上传 APK 并提审。** 请先核对应用、产物和渠道范围的授权。

## 阅读入口

| 任务 | 文档 |
| --- | --- |
| 安装、配置、输入凭据 | [首次使用](docs/colleague-quickstart.md) |
| 准备批次、提交、查询和恢复 | [日常发布](docs/daily-cli-workflow.md) |
| 理解审核、上架和查询状态 | [自动查询说明](docs/channels/automatic-observation.md) |
| 修改代码、测试或提交 PR | [贡献指南](CONTRIBUTING.md) |
| 查看状态机和渠道依据 | [架构](docs/architecture.md)、[协议参考](docs/channels/protocol-evidence.md) |
| 打包或发布 npm 产物 | [构建与分发](docs/mac-cli-delivery.md)、[版本发布](docs/release-process.md) |

0.1.x Go 版的本地状态不会迁入新版。新版数据保存在 `~/Library/Application Support/AndroidPublisherCLI/v2/`。迁移范围见[更新记录](CHANGELOG.md)。

## 许可证

原创代码采用 [MIT 许可证](LICENSE)。依赖和协议来源见[第三方声明](THIRD_PARTY_NOTICES.md)。本项目独立于各渠道厂商。
