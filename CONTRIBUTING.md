# 贡献指南

本页供修改 CLI、测试和文档的贡献者使用。操作发布命令请读[首次使用](docs/colleague-quickstart.md)和[日常发布](docs/daily-cli-workflow.md)。

## 开发环境

安装 [`.node-version`](.node-version) 指定的 Node.js。仓库使用 `package-lock.json` 固定 npm 依赖。在仓库根目录运行：

```sh
npm ci --ignore-scripts
npm run check
```

`npm run check` 先检查 TypeScript 类型，再构建 CLI 并运行 Node.js 测试。真实 APK 测试需要 Android SDK Build Tools 和 JDK。CI 会安装独立工具，并设置 `PUBLISHER_REQUIRE_ANDROID_TESTS=1`。本机执行完整验收时也设置这个变量：

```sh
PUBLISHER_REQUIRE_ANDROID_TESTS=1 npm run check
```

工具缺失时，完整验收应失败。普通本机测试会跳过真实 APK 用例；测试结果会明确显示跳过。合成渠道测试使用本地响应，不会访问商店或提交应用。

## 按改动检查

| 改动 | 先读 | 验收重点 |
| --- | --- | --- |
| 批次、执行、状态或恢复 | [架构](docs/architecture.md) | 写入前已有持久意图；响应丢失后重启不能重复发送 |
| 渠道请求或观测 | [协议参考](docs/channels/protocol-evidence.md)、[自动查询说明](docs/channels/automatic-observation.md) | 使用准确的官方字段；分别测试成功、错误和无法关联的结果 |
| npm 打包与交付 | [构建与分发](docs/mac-cli-delivery.md) | 用实际 tarball 安装；核对文件清单、命令版本和依赖声明 |
| 文档 | 本页“文档归属” | 保留命令、条件、链接和验证程度 |

渠道协议由本仓库独立实现。使用第三方代码前，先确认许可证。测试、示例和截图使用 `com.example.app`、`example.invalid` 等合成内容。真实账号、APK、私钥、Cookie、签名 URL 和原始响应不能进入 Git、issue、构建产物或普通日志。

## 分支与 PR

代码 agent 使用 `codex/topic` 分支。提交前，在本仓库配置 GitHub noreply 邮箱，并确认 GitHub 网页设置隐藏邮箱地址。按实际改动运行检查，向 `main` 创建 PR。说明行为变化、检查结果、恢复影响和仍待验证的渠道条件。

合并前，以待合并提交上的必需 CI `check` 为准。解决实质性审查意见后，使用 squash 合并。仓库规则禁止直接推送、强制推送或删除 `main`。PR 合并不授权 Android 应用提审。

CI 工作流见 [check.yml](https://github.com/wangyiliangnet/android-publisher-cli/blob/main/.github/workflows/check.yml)。它运行公开源码扫描、TypeScript 检查、合成协议测试、真实 APK 签名测试、子进程崩溃恢复测试，以及 npm 安装检查。CI 只使用临时身份和合成材料。

## 文档归属

| 内容 | 主要位置 |
| --- | --- |
| 项目用途和阅读入口 | [README](README.md) |
| 安装、配置和凭据输入 | [首次使用](docs/colleague-quickstart.md) |
| 每次发布、查询和恢复 | [日常发布](docs/daily-cli-workflow.md) |
| 查询状态的含义 | [自动查询说明](docs/channels/automatic-observation.md) |
| 执行约束和本地状态 | [架构](docs/architecture.md) |
| 接口能力及来源 | [协议参考](docs/channels/protocol-evidence.md) |
| 打包和版本交付 | [构建与分发](docs/mac-cli-delivery.md)、[版本发布](docs/release-process.md) |

写操作步骤时，先列准备材料，再给命令。命令后说明成功的判断依据，以及失败时该做什么。审核、上架和查询健康状态分别描述，不能把“已受理”写成“已上架”。
