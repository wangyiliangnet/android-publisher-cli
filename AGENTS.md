# 代码 agent 协作规则

本仓库维护面向 Apple Silicon Mac 发版同事的公开 CLI。先按变更范围读取对应资料：

- 开发、测试或提交 PR：读取 CONTRIBUTING.md，按其中的规则验收和合并。
- 修改执行、状态、并发或恢复：docs/architecture.md。
- 修改渠道协议或观测：读取 docs/channels/protocol-evidence.md。涉及用户状态展示时，另读 docs/channels/automatic-observation.md。区分已实现、合成测试通过和真实账号已验证。
- 修改打包、签名或版本交付：docs/mac-cli-delivery.md 和 docs/release-process.md。

## 必须保持的约束

- 业务写入前，冻结意图和产物。请求前，持久化意图。取得可验证的回执后，立即保存回执。同一渠道、账号和应用存在未决写入时，阻止冲突写入。仅在核对确认或协议幂等性已验证后继续。
- plan 离线执行。预检、观测和核对只读取发布数据。令牌生命周期操作通过账号协调层执行。
- 分别记录执行、审核、上架和查询健康状态。未关联信息与推测不得确认目标状态或消除证据争议。
- 真实商店变更需获得覆盖应用、产物和发布范围的授权。初始化、测试、CI 和合并 PR 均不构成提审授权。
- 凭据在本机通过钥匙串引用读取。真实账号材料、APK、签名私钥、Cookie、签名 URL 和敏感响应不得进入 Git、issue、agent 提示词、构建产物或常规日志。运行状态保存在 ~/Library/Application Support/AndroidPublisherCLI/，安装文件保存在 ~/.local/android-publisher-cli/。
- 跟踪文件、报告示例和 CI 使用合成数据。CI 使用临时凭据与独立浏览器配置。独立实现渠道协议。复用第三方代码前，确认许可条件。

获准使用 subagents 时，限定路径和问题范围。审查者默认只读。协调者负责整合修订，并记录未解决问题。
