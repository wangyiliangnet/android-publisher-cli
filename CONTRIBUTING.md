# 贡献指南

修改 CLI、测试或文档前，先按改动范围读取对应资料。操作者的安装和发布步骤从 [README](README.md) 进入。

## 开发与测试

安装 [`.node-version`](.node-version) 指定的 Node.js。仓库用 `package-lock.json` 固定 npm 依赖：

```sh
npm ci --ignore-scripts
npm run check
```

`npm run check` 依次检查 TypeScript 类型、构建 CLI 并运行 Node.js 测试。真实 APK 测试需要 Android SDK Build Tools 和 JDK；本机完整验收时设置：

```sh
PUBLISHER_REQUIRE_ANDROID_TESTS=1 npm run check
```

工具缺失时，完整验收应失败。普通本机测试会明确显示跳过的真实 APK 用例。合成渠道测试使用本地响应，不访问商店或提交应用。从源码试用 CLI 时，运行 `npm run build` 后可执行 `npm link`。

## 按改动检查

| 改动 | 先读 | 验收重点 |
| --- | --- | --- |
| 批次、执行、状态或恢复 | [架构说明](docs/architecture.md) | 请求前保存意图；响应丢失后重启不能重复发送 |
| 渠道请求或观测 | [渠道协议](docs/channels/protocol-evidence.md)、[状态查询与恢复](docs/status-and-recovery.md) | 核对官方字段，覆盖成功、错误和无法关联的结果 |
| npm 打包与发布 | [npm 包验收](docs/npm-package.md)、[CLI 版本发布](docs/npm-release.md) | 安装实际 tarball，核对文件清单、命令和版本 |
| 文档 | [README](README.md) 的阅读入口 | 核对命令、条件、链接与验证程度 |

渠道协议由本仓库独立实现。复用第三方代码前，先确认许可证。测试、示例和截图使用 `com.example.app`、`example.invalid` 等合成内容。真实账号、APK、私钥、Cookie、签名 URL 和原始响应不能进入 Git、issue、构建产物或普通日志。

## 分支与 PR

代码 agent 使用 `codex/topic` 分支。提交前，在本仓库配置 GitHub noreply 邮箱，并确认 GitHub 网页设置隐藏邮箱地址。按实际改动运行检查，向 `main` 创建 PR。说明行为变化、检查结果、恢复影响和仍待验证的渠道条件。

合并前，以待合并提交上的必需 CI `check` 为准。解决实质性审查意见后，使用 squash 合并。仓库规则禁止直接推送、强制推送或删除 `main`。PR 合并不授权 Android 应用提审。

[check.yml](.github/workflows/check.yml) 运行公开源码扫描、TypeScript 检查和测试。测试覆盖合成协议、真实 APK 签名、子进程崩溃恢复及 npm 安装。CI 只使用临时身份与合成材料。

CLI 版本通过 [Prepare npm release](.github/workflows/prepare-npm-release.yml) 创建 PR。维护者合并后发布对应 GitHub Release，触发 [Publish npm package](.github/workflows/publish-npm.yml)。操作顺序见 [CLI 版本发布](docs/npm-release.md)。

写操作步骤时，先列准备材料，再给命令。命令后说明成功的判断依据和失败时的处理。分别描述执行、审核、上架与查询健康状态。
