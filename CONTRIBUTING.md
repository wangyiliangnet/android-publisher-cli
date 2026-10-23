# 贡献指南

先用一个范围明确的 issue 描述行为和验收条件。不要包含真实凭据、APK、渠道响应、私有账号数据或签名 URL。示例和测试数据使用 `com.example.app`、`example.invalid` 及明确的合成标识。

## 本地开发流程

开始贡献前，为本仓库配置 GitHub noreply 提交邮箱，并在 [GitHub 邮箱设置](https://github.com/settings/emails)中开启 **Keep my email addresses private（隐藏邮箱地址）**。GitHub 网页提交身份与本地 Git 分开配置，也会影响自动生成的 PR 合并元数据。公开信息检查会核验这两种身份，自动生成的合并提交也不例外。配置方法见 [GitHub 提交邮箱说明](https://docs.github.com/en/account-and-profile/how-tos/email-preferences/setting-your-commit-email-address)。

1. 从最新 `main` 创建 `feat/short-topic`、`fix/short-topic` 或 `docs/short-topic` 分支。自动化变更可使用 `codex/short-topic`。
2. 修改发布行为前，阅读 [AGENTS.md](AGENTS.md)、[架构说明](docs/architecture.md)和[路线图](docs/execution-plan.md)。
3. 安装固定版本的 Go、Node.js 和 npm。真实签名测试还需 Android SDK Build Tools 和 JDK。
4. 先运行 `make browser-install`，再运行 `PUBLISHER_REQUIRE_ANDROID_TESTS=1 make check`。开发过程中按需执行专项测试。
5. 向 `main` 提交 PR，说明用户可见的变化、测试、协议依据和遗留限制，关联相关 issue 并更新文档。
6. 解决审查讨论，等待必需 CI 通过后采用 squash 合并，让每项变更在 main 上对应一个清晰的提交，再删除已合并的工作分支。

`main` 要求通过 PR 合并、必需检查通过且审查讨论已解决。仓库规则禁止直接推送、强制推送和删除 main。项目初期只有一名维护者，因此暂不强制第二名人工批准；发布、凭据、传输、恢复和版本交付变更仍需独立审查。增加维护者后，应提高必需批准数量。

## 发布与恢复变更

`plan` 保持离线。只读预检、核对和观测不得写入发布数据；令牌管理作为账号协调下的生命周期操作处理。

写入前冻结意图和产物，派发前保存意图，验证响应后再记录成功。超时、进程退出或响应丢失都不能证明渠道拒绝了请求。结果未知的写入会阻止其他发布中的冲突写入。

修改写入路径时补充故障注入测试。验证崩溃恢复需真实终止子进程，不能只用返回错误的模拟测试替代。自动采集的信息、审核推测、过期或未关联的信息不得计入已核实的目标数量，也不得作为核对结论。

以渠道官方文档作为协议依据，记录准确来源，说明未被文档覆盖或尚未通过账号验证的行为。复制第三方实现前，先检查许可证并在需要时取得授权。

## 安全与版本发布

漏洞报告方式见 [SECURITY.md](SECURITY.md)。打包、标签、草稿和公开发布条件见[发布流程](docs/release-process.md)。CI 不会向应用商店上传应用。

贡献采用仓库的 [MIT 许可证](LICENSE)，并需保留适用的依赖及第三方声明。
