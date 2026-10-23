# Android 发布工具（Android Publisher CLI）

[![CI](https://github.com/wangyiliangnet/android-publisher-cli/actions/workflows/check.yml/badge.svg?branch=main)](https://github.com/wangyiliangnet/android-publisher-cli/actions/workflows/check.yml)

在 Apple Silicon Mac 上运行的本地发布工具，支持华为应用市场、荣耀、小米、OPPO、vivo 和应用宝。

在同一台 Mac 上准备已签名的 APK、审阅不可变的发布计划、向选定渠道提交更新，并查看发布历史。凭据保存在 macOS 钥匙串中，程序名为 `release`。

**当前为公开测试版。** 协议与恢复行为已通过合成数据验收；这不代表已核实任何开发者账号、应用、渠道权限或真实发布。请先使用测试应用，并在允许业务写入前审阅每份发布计划。

## 从这里开始

- [Mac 同事使用手册](docs/colleague-quickstart.md)
- [日常操作与恢复](docs/daily-cli-workflow.md)
- [Mac 安装与分发](docs/mac-cli-delivery.md)
- [渠道能力与验证依据](docs/channels/protocol-evidence.md)
- [架构](docs/architecture.md)、[路线图](docs/execution-plan.md)和[贡献指南](CONTRIBUTING.md)
- [公开测试版验收记录](docs/public-beta-acceptance.md)

检查真实 APK 需要 Apple Silicon Mac、Android SDK Build Tools 和兼容的 JDK。使用安装包执行 API 发布命令时，无需安装 Go 或 Node.js。浏览器观测器目前用于模拟和验收，日常发布无需先登录浏览器。

```sh
release version --json
release doctor
release setup --project demo --apk "/absolute/path/trusted.apk"
```

配置向导使用中文提示，只保存公开的账号标识和钥匙串条目名称。密钥需另行通过终端隐藏输入或私有凭据文件导入。不要把凭据粘贴到 issue、PR 或命令行参数中。

每次发布都需明确提供 APK 和更新说明，完成只读预检，再审阅保存的批准文件。提交时必须使用该次审阅的准确批准摘要。完整步骤见[日常操作指南](docs/daily-cli-workflow.md)。

## 发布过程的约束

- 业务写入前，冻结 APK、签名、更新说明、目标和发布规则。
- 请求发出前保存操作意图，取得可验证的回执后立即保存。
- 响应丢失时，操作结果保持未决，直到核对取得足够依据。重启程序不能作为再次写入的理由。
- 执行、审核、上架和查询健康状态分别记录。“已受理”不等于“审核通过”或“已上架”。
- 小米的审核推测、应用宝尚未关联到本次版本的信息单独标注，不计入已核实的审核通过或上架数量。
- 缓存历史和报告可离线读取。日常状态刷新使用 API 和应用宝公开商店页面，无需先登录浏览器或手工录入状态。

本地状态保存在 `~/Library/Application Support/AndroidPublisherCLI/`；凭据服务使用 `io.github.wangyiliangnet.android-publisher-cli.v1`。运行文件、签名 URL、凭据和 APK 不应提交到 Git。

## 构建与验证

使用 [`.go-version`](.go-version) 和 [`.node-version`](.node-version) 指定的 Go、Node.js 版本，以及[浏览器依赖配置](browser/package.json)中的 npm 版本。

```sh
make browser-install
PUBLISHER_REQUIRE_ANDROID_TESTS=1 make check
```

`make check` 包括格式检查、静态检查、Go 竞态测试、浏览器验收和本机构建。Android 签名集成测试需要 SDK 和 JDK；CI 会提供这些环境，并强制执行测试，缺少依赖时不会静默跳过。测试使用合成输入和临时凭据，不使用渠道生产账号或真实应用 APK。

分支保护、必需 CI、版本标签和草稿发布审阅流程见[发布流程](docs/release-process.md)。

## 许可证

项目原创代码采用 [MIT 许可证](LICENSE)，另附[中文译文](LICENSE.zh-CN.md)。依赖保留各自的许可证，渠道服务和文档适用各自的条款。本项目独立于各渠道厂商。来源和实现边界见[协议验证依据](docs/channels/protocol-evidence.md)。
