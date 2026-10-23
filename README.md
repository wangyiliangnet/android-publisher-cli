# Android 发布工具（Android Publisher CLI）

[![CI](https://github.com/wangyiliangnet/android-publisher-cli/actions/workflows/check.yml/badge.svg?branch=main)](https://github.com/wangyiliangnet/android-publisher-cli/actions/workflows/check.yml)

面向负责 Android 发版的 Mac 同事，在本机向华为、荣耀、小米、OPPO、vivo 和应用宝提交版本更新。支持 Apple Silicon，程序名为 `release`；账号凭据保存在 macOS 钥匙串，APK 和发布记录保留在本机。

当前为公开测试版，支持“审核通过后立即发布”的更新流程。CI 预览包使用 ad-hoc 临时签名，未公证；使用真实凭据前，应由维护者提供固定签名包，并先验证测试应用及账号权限。合成测试通过不代表已核实真实渠道权限。

## 按你的工作选择入口

| 读者 | 要完成的事 | 文档 |
| --- | --- | --- |
| 发版同事，首次使用 | 安装、环境检查、项目配置、导入凭据、升级与卸载 | [首次使用](docs/colleague-quickstart.md) |
| 已配置好项目的发版同事 | 准备 APK、审阅提交、查询、报告、异常恢复与备份 | [日常发布](docs/daily-cli-workflow.md) |
| 查询状态的同事 | 理解小米推测、应用宝版本关联、部分失败和缓存 | [状态解释](docs/channels/automatic-observation.md) |
| 代码贡献者 | 配置开发环境、测试、提交 PR | [贡献指南](CONTRIBUTING.md) |
| 修改发布内核或渠道适配器的贡献者 | 理解状态、写入恢复和协议依据 | [架构](docs/architecture.md)、[协议参考](docs/channels/protocol-evidence.md) |
| 版本维护者 | 构建签名包、准备草稿、核验产物并分发 | [构建与分发](docs/mac-cli-delivery.md)、[版本发布](docs/release-process.md) |

后续计划见[维护路线图](docs/execution-plan.md)。代码 agent 从 [AGENTS.md](AGENTS.md) 开始；漏洞报告方式见[安全政策](SECURITY.md)。

## 许可证

项目原创代码采用 [MIT 许可证](LICENSE)，另附[中文译文](LICENSE.zh-CN.md)。依赖和协议参考来源保留各自的许可条款，见[第三方声明](THIRD_PARTY_NOTICES.md)。本项目独立于各渠道厂商。
