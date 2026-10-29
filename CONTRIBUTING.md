# 贡献指南

本页面向修改代码和文档的贡献者。使用工具发布 Android 应用，请从 [README](README.md) 开始。提交改动前，你需要验证命令行为、两端共享的发布状态，以及中断后是否可能重复提交。

## 准备开发环境

安装 [`.node-version`](.node-version) 指定的 Node.js，使用 `package-lock.json` 中锁定的依赖版本：

```sh
npm ci --ignore-scripts
npm run check
```

`npm run check` 依次执行 TypeScript 类型检查、构建和 Node.js 测试。测试使用临时文件与模拟商店响应，不访问真实商店或提交应用。

需要验证真实 APK 的检查与签名时，按[安装说明](docs/getting-started.md#贡献者生成测试-apk)准备 SDK Build-Tools、Command-line Tools、SDK Platform 和 JDK，然后运行：

```sh
PUBLISHER_REQUIRE_ANDROID_TESTS=1 npm run check
```

完整验收要求真实 APK 用例通过；工具缺失会失败。普通本机检查在工具缺失时会明确跳过该用例。测试中的 APK 和签名证书均临时生成，不使用真实应用材料。

从源码试用 CLI，先运行 `npm run build`，再执行 `node build/cli.js --help`。需要在终端直接使用命令名时，可运行 `npm link`。

## 按改动范围查资料

| 改动范围 | 先读 | 验证重点 |
| --- | --- | --- |
| 安装、诊断、配置向导 | [安装](docs/getting-started.md)、[应用配置](docs/configuration.md) | 缺失工具有修复方法，输入说明明确，重配保留已有账号备注 |
| 发布批次、执行、状态或恢复 | [架构说明](docs/architecture.md)、[查询与恢复](docs/status-and-recovery.md) | 请求前保存意图，响应丢失后重启不能重复发送 |
| 渠道请求与查询 | [渠道接口](docs/channels/protocol-evidence.md) | 核对官方字段，覆盖成功、错误与无法关联本次版本的结果 |
| npm 打包与发布 | [安装包验收](docs/npm-package.md)、[CLI 发版](docs/npm-release.md) | 安装实际压缩包，检查文件清单、命令入口和版本 |
| 使用文档 | [README](README.md) 的阅读入口 | 先说明任务和作用，再给步骤、成功判断和失败处理 |

每个用户概念使用[领域词汇表](GLOSSARY.md)中的名称。用户文档称 `project` 为“应用配置”，`batch` 为“发布批次”，`target` 为“渠道目标”，`approval` 为“提交计划”，并在首次出现时对应到命令字段。维护文档保留必要的状态、表和类型名称。

## 分支、检查与 PR

代码 agent 使用 `codex/topic` 分支，并在本仓库配置 GitHub noreply 提交邮箱。不要直接推送、强制推送或删除 `main`。

按改动运行检查，向 `main` 创建 PR。说明解决的问题、变更后的行为、实际完成的验证，以及兼容性和仍待验证的渠道条件。合并前，以待合并提交的必需 CI `check` 为准；处理实质性审查意见后 squash 合并。

[check.yml](.github/workflows/check.yml) 会检查公开源码、工作流、TypeScript 和测试，并安装实际 npm 包验证入口。CI 只使用临时身份与合成材料。

渠道协议由本仓库独立实现，复用第三方代码前须核对许可证。真实账号、APK、私钥、Cookie、带签名的上传地址和原始响应不能进入 Git、issue 或构建产物。示例使用 `com.example.app`、`example.invalid` 等虚构资料。

发布本工具的新版本，按 [CLI 发版流程](docs/npm-release.md)创建版本 PR 和 GitHub Release。代码测试、PR、打包或 CLI 发版均不构成 Android 应用提审授权。

## 浏览器回归

Web 凭据表单有独立的合成数据浏览器回归。普通检查跳过此项；本机已提供 Playwright 和 Chrome 时，可以启用：

```sh
PUBLISHER_BROWSER_TESTS=1 PUBLISHER_BROWSER_CHANNEL=chrome npm run check
```

Playwright 不在默认模块路径时，用 `PUBLISHER_PLAYWRIGHT_MODULE` 指定其 `index.mjs` 入口。回归使用新的浏览器上下文和临时状态，检查多行材料、输入清空、错误脱敏、重复提交和组件修复，不连接真实商店。
