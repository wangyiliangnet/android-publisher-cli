# 贡献指南

修改代码、测试或文档时，按本页准备环境、选择检查并提交 PR。使用 CLI 发版，请从[首次使用](docs/colleague-quickstart.md)或[日常发布](docs/daily-cli-workflow.md)开始。制作和发布 CLI 安装包见[版本发布](docs/release-process.md)。

## 开始开发

### 1. 说明要解决的问题

先在 issue 中写清问题和验收条件。示例、测试数据和截图使用 `com.example.app`、`example.invalid` 等合成内容。真实账号材料、APK、渠道原始响应和签名 URL 不得上传。

### 2. 配置公开提交身份

为本仓库设置 GitHub noreply 提交邮箱。再到 [GitHub 邮箱设置](https://github.com/settings/emails)开启 **Keep my email addresses private（隐藏邮箱地址）**。

网页身份与本地 Git 分开配置，两处都要检查。网页设置也影响 GitHub 生成的 PR 合并元数据。公开信息检查会检查这些元数据。邮箱格式和配置方法见 [GitHub 提交邮箱说明](https://docs.github.com/en/account-and-profile/how-tos/email-preferences/setting-your-commit-email-address)。

### 3. 安装固定版本的工具

按 [`.go-version`](.go-version)、[`.node-version`](.node-version) 和 [browser/package.json](browser/package.json) 安装指定的 Go、Node.js 和 npm。完整验收还需要 Android SDK Build Tools 和 JDK。

在仓库根目录安装浏览器测试依赖：

```sh
make browser-install
```

### 4. 运行基线检查

```sh
PUBLISHER_REQUIRE_ANDROID_TESTS=1 make check
```

`make check` 会检查格式和静态问题，运行 Go 竞态、真实浏览器、子进程恢复及打包脚本检查，最后构建程序。Android 集成测试还会验证生成 APK 的签名。测试使用合成数据和临时身份。

检查失败时，先处理报错。必需环境缺失也算失败，不能静默跳过。若 Go 工具链不在 PATH 中，运行命令时，将 `GO` 和 `GOFMT` 分别设为对应工具的绝对路径。

## 按变更选择验收

开发时先运行与改动相关的检查。Go 代码改完后，还要完成完整 `make check`。

| 改了什么 | 先读什么 | 需要确认什么 |
| --- | --- | --- |
| 执行、状态、并发或恢复 | [架构](docs/architecture.md) | 注入响应丢失并真实终止子进程，确认重启不重复业务写入；仅返回错误的 mock 不足以证明崩溃恢复 |
| 渠道协议或 HTTP | [协议参考](docs/channels/protocol-evidence.md) | 记录准确官方来源、字段、访问限制和未验证行为，补充合成成功及错误响应测试 |
| 观测和状态展示 | [状态解释](docs/channels/automatic-observation.md) | 覆盖缺失字段、旧版本、未知码、部分失败、缓存和争议；参考信息不能改变已核实数量或解除写入阻塞 |
| 安装、签名或打包 | [构建与分发](docs/mac-cli-delivery.md) | 核对产物清单，验收安装、升级、回滚、卸载，以及篡改和符号链接拒绝行为 |
| 文档 | 本页的“文档维护约定” | 明确读者和内容归属，检查引用与锚点；随包文档还要验收离线链接和实际打包结果 |

凭据、传输、发布、恢复、公开数据和工作流变更需独立审查。合并或发布前，解决实质性发现。协议实现与第三方许可要求见[协议参考](docs/channels/protocol-evidence.md)和[第三方声明](THIRD_PARTY_NOTICES.md)。

## 分支与合并

1. 更新本地 `main`，从最新提交创建 `feat/topic`、`fix/topic` 或 `docs/topic`。代码 agent 使用 `codex/topic`。
2. 完成修改和相关检查后，向 `main` 创建 PR。说明解决的问题、变更后的行为和实际验证结果。关联相关 issue，说明兼容或恢复影响及遗留限制。更新受影响文档。
3. 等待最新待合并提交的必需 CI **`check`** 通过，解决审查讨论。
4. 使用 squash 合并，再删除工作分支。

仓库禁止直接推送、强制推送或删除 `main`。工作分支必须保持最新。旧提交上的成功检查不能代替待合并提交的检查。

目前只有一名人工维护者，必需批准数量为零。第二名维护者加入后，应要求至少一名人工批准。subagent 审查可作为补充依据，不能代替 GitHub 人工批准。

### CI 做什么

[CI 工作流](.github/workflows/check.yml)在 PR、main 推送和手动触发时运行。运行器、固定依赖和实际步骤都以该文件为准。常规检查仅有仓库只读权限。新运行会取消它所取代的旧检查。

Actions 固定到上游完整 SHA，生产渠道凭据不进入 CI。合并 PR 不授权 Android 应用上传或提审。

## 文档维护约定

每份内容只维护在一个主要位置，其他页面链接过去。按下面的归属放置内容，避免复制同一套教程：

| 内容 | 放在哪里 |
| --- | --- |
| 项目用途和阅读入口 | [README](README.md) |
| 安装、首次配置、升级和卸载 | [首次使用](docs/colleague-quickstart.md) |
| 每次发版、查询、报告、恢复和备份 | [日常发布](docs/daily-cli-workflow.md) |
| 查询状态的含义 | [自动查询说明](docs/channels/automatic-observation.md) |
| 实现原理 | [架构](docs/architecture.md) |
| 接口能力和来源 | [协议参考](docs/channels/protocol-evidence.md) |
| 后续工作 | [路线图](docs/execution-plan.md) |
| 历史验收 | `docs/archive/` |

安装包 README 只提供离线导航。写教程时，按读者实际操作的顺序组织：先说明准备材料，再给命令。命令后说明如何判断结果，以及失败后如何处理。

文档借鉴 [ASD-STE100 的写作原则](https://www.asd-ste100.org/about.html)，按中文阅读习惯应用，不声明符合该英语规范：

- 用短句交代一个主要意思。操作句直接写明动作；有先后顺序的动作分句说明。
- 先说明适用条件，再给出操作。将操作与结果分开写，明确成功或失败的判断依据。
- 同一概念使用同一术语。区分提交受理、审核通过和公开上架，避免用“成功”概括不同状态。
- 修改文字时保留原有条件、否定、范围和验证程度。命令、字段、路径、链接目标和标题锚点保持准确。

## 安全与许可

漏洞通过[安全政策](SECURITY.md)指定的私密渠道报告。贡献采用 [MIT 许可证](LICENSE)，并保留适用的依赖与第三方声明。版本号、标签和草稿的操作步骤见[版本发布](docs/release-process.md)。
