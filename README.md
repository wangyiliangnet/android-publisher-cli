# Android 发布工具（Android Publisher CLI）

[![CI](https://github.com/wangyiliangnet/android-publisher-cli/actions/workflows/check.yml/badge.svg?branch=main)](https://github.com/wangyiliangnet/android-publisher-cli/actions/workflows/check.yml)

在 Apple Silicon Mac 上，用 `release` 向华为、荣耀、小米、OPPO、vivo 和应用宝提交 Android 版本更新。账号凭据保存在 macOS 钥匙串，APK 和发布记录保留在本机。

日常操作按六个渠道一起发布编写：准备各渠道 APK 和更新说明，检查账号与应用，审阅本次提交，再上传提审。提交后可以查询进度、导出报告；中途出错时先核对结果，再继续。

当前为公开测试版，只支持已有应用的版本更新，发布规则为“审核通过后立即发布”。**使用真实凭据前，请向维护者取得固定签名包，并先用测试应用确认账号权限。** CI 预览包使用 ad-hoc 临时签名，未公证。合成测试通过不代表真实渠道权限已验证。

## 按你的工作选择入口

| 你要做什么 | 从哪里开始 | 完成后能做什么 |
| --- | --- | --- |
| 第一次使用 | [首次使用](docs/colleague-quickstart.md) | 安装工具，配置六个渠道并导入凭据 |
| 发布一个新版本 | [日常发布](docs/daily-cli-workflow.md) | 逐步完成准备、预检、审阅、提交和查询 |
| 看不懂查询结果 | [状态解释](docs/channels/automatic-observation.md) | 区分已受理、审核、上架、推测和缓存 |
| 修改代码或文档 | [贡献指南](CONTRIBUTING.md) | 配置开发环境，完成检查并提交 PR |
| 修改发布逻辑或渠道接口 | [架构](docs/architecture.md)、[协议参考](docs/channels/protocol-evidence.md) | 查找模块职责、恢复约束和接口依据 |
| 给同事制作安装包或发布 CLI 版本 | [构建与分发](docs/mac-cli-delivery.md)、[版本发布](docs/release-process.md) | 构建签名包，核验产物并复核发布草稿 |

后续计划见[维护路线图](docs/execution-plan.md)。代码 agent 从 [AGENTS.md](AGENTS.md) 开始；漏洞报告方式见[安全政策](SECURITY.md)。

## 许可证

项目原创代码采用 [MIT 许可证](LICENSE)，另附[中文译文](LICENSE.zh-CN.md)。依赖和协议参考来源保留各自的许可条款，见[第三方声明](THIRD_PARTY_NOTICES.md)。本项目独立于各渠道厂商。
