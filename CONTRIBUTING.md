# 贡献指南

面向修改代码、测试和文档的贡献者。使用 CLI 发版请从[首次使用](docs/colleague-quickstart.md)或[日常发布](docs/daily-cli-workflow.md)开始；维护者的交付操作见[版本发布](docs/release-process.md)。

## 开始开发

先在 issue 中说明要解决的问题和验收条件。示例、测试数据和截图使用 `com.example.app`、`example.invalid` 等合成内容；不要上传真实账号材料、APK、渠道原始响应或签名 URL。

为本仓库配置 GitHub noreply 提交邮箱，并在 [GitHub 邮箱设置](https://github.com/settings/emails)中开启 **Keep my email addresses private（隐藏邮箱地址）**。网页身份与本地 Git 分开配置，也影响 GitHub 生成的 PR 合并元数据，两者都受公开信息检查。具体步骤见 [GitHub 提交邮箱说明](https://docs.github.com/en/account-and-profile/how-tos/email-preferences/setting-your-commit-email-address)。

安装 [`.go-version`](.go-version)、[`.node-version`](.node-version) 和 [browser/package.json](browser/package.json) 指定的 Go、Node.js 与 npm。完整验收还需要 Android SDK Build Tools 和 JDK。

```sh
make browser-install
PUBLISHER_REQUIRE_ANDROID_TESTS=1 make check
```

使用 PATH 外的 Go 工具链时，同时设置 `GO` 和 `GOFMT` 为绝对路径。`make check` 包括格式、静态检查、Go 竞态测试、真实浏览器、子进程恢复、打包脚本检查和构建；Android 集成测试还验证生成 APK 的签名。必需环境缺失时应失败，不能静默跳过。测试使用合成数据和临时身份。

## 按变更选择验收

| 变更 | 阅读与验证要求 |
| --- | --- |
| 执行、状态、并发或恢复 | 阅读[架构](docs/architecture.md)。注入响应丢失并真实终止子进程，确认重启不重复业务写入；仅返回错误的 mock 不足以证明崩溃恢复 |
| 渠道协议或 HTTP | 阅读[协议参考](docs/channels/protocol-evidence.md)，记录准确官方来源、字段、访问限制及未验证行为，补充合成成功和错误响应测试 |
| 观测和状态展示 | 阅读[状态解释](docs/channels/automatic-observation.md)，覆盖缺失字段、旧版本、未知码、部分失败、缓存与争议；参考信息不得改变已核实数量或写入屏障 |
| 安装、签名或打包 | 阅读[构建与分发](docs/mac-cli-delivery.md)，检查产物清单并验收安装、升级、回滚、卸载、篡改和符号链接拒绝行为 |
| 文档 | 明确读者和唯一内容归属，检查引用与锚点；涉及随包文档时验收离线链接和打包结果 |

开发时运行专项检查；Go 变更还需完成完整 `make check`。凭据、传输、发布、恢复、公开数据和工作流变更需独立审查，合并或发布前解决实质性发现。协议实现和第三方许可边界以[协议参考](docs/channels/protocol-evidence.md)及[第三方声明](THIRD_PARTY_NOTICES.md)为准。

## 分支与合并

1. 从最新 `main` 创建 `feat/topic`、`fix/topic` 或 `docs/topic`；代码 agent 使用 `codex/topic`。
2. 向 `main` 提交 PR，说明行为、验证、相关 issue、兼容或恢复影响及遗留限制，并更新受影响文档。
3. 等待最新待合并提交的必需 CI **`check`** 通过，解决审查讨论后采用 squash 合并，再删除工作分支。

仓库规则禁止直接推送 main、强制推送和删除 main。分支必须保持最新，不得用无关旧检查绕过失败。项目目前只有一名人工维护者，必需批准数量为零；第二名维护者加入后，应要求至少一名人工批准。subagent 审查属于补充依据，不等于 GitHub 人工批准。

[CI 工作流](.github/workflows/check.yml)是运行器、固定依赖和实际步骤的配置来源：在 PR、main 推送及手动触发时执行，常规检查仅有仓库只读权限，取消被新运行取代的旧检查。Actions 固定到上游完整 SHA，生产渠道凭据不进入 CI。合并 PR 不授权应用上传或提审。

## 文档维护约定

[README](README.md)只介绍项目与阅读入口。首次配置归使用手册，每次发版归日常流程，状态含义归自动查询说明；不要在多份文档复制相同命令教程。实现原理归架构，接口与来源归协议参考，未来事项归路线图，历史验收归归档。安装包 README 只提供离线导航。

## 安全与许可

漏洞通过[安全政策](SECURITY.md)指定的私密渠道报告。贡献采用 [MIT 许可证](LICENSE)，保留适用的依赖和第三方声明。版本号、标签和草稿的操作步骤见[版本发布](docs/release-process.md)。
