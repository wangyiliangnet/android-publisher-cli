# 代码 agent 协作规则

仓库使用 TypeScript、Node.js 和 npm。按改动范围读取资料：

- 开发、测试或提交 PR：先读 [CONTRIBUTING.md](CONTRIBUTING.md)。
- 修改批次、执行、状态或恢复：先读 [架构](docs/architecture.md)。
- 修改渠道协议：先读[渠道协议](docs/channels/protocol-evidence.md)。修改状态展示时，再读[状态查询与恢复](docs/status-and-recovery.md)。
- 修改 npm 打包或版本交付：先读[npm 包验收](docs/npm-package.md)和[CLI 版本发布](docs/npm-release.md)。

## 必须保持的约束

- 业务写入前冻结批次和 APK。每个请求发出前保存操作意图。取得可验证回执后立即保存。结果未决时阻止同一渠道、账号和应用的冲突写入。
- `prepare` 和 `batch review` 离线执行。预检、观测和核对只读取发布数据。令牌申请与刷新经过账号状态记录。
- 分别记录执行、审核、上架和查询健康状态。未关联信息不能确认目标状态，也不能解除未决写入。
- 真实商店变更需要覆盖应用、产物和渠道范围的授权。初始化、测试、CI、PR 或 npm 打包均不构成提审授权。
- 渠道凭据由用户在终端或受会话保护的本地 Web 表单中录入。本机 JSON 和 PEM 材料文件必须属于当前用户，权限为 `0600`。原始输入只在当前操作的内存中处理，凭据加密保存在本机；列表与接口响应只显示接入状态。令牌和上传授权只写入本机私有状态。真实凭据、APK、私钥、Cookie、签名 URL 和敏感响应不得进入 Git、issue、agent 提示词、包或普通日志。
- 本机状态位于 `~/Library/Application Support/AndroidPublisherCLI/v2/`。测试和 CI 使用合成数据。渠道协议独立实现；复用第三方代码前核对许可证。

有 `.codegraph/` 索引时，定位代码先用 `codegraph explore`。没有该目录就直接使用仓库搜索工具。

获准使用 subagents 时，限定路径和问题范围。审查者默认只读。协调者负责整合修订，并记录未解决问题。
