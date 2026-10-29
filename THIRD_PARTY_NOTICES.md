# 第三方声明

本页记录代码、依赖和接口研究资料的来源及许可，供维护者复用代码和分发安装包前核对。项目原创代码采用 [MIT 许可证](LICENSE)。

## 构建与运行工具

npm 构建依赖的准确版本和完整性校验值记录在 `package-lock.json`。TypeScript 使用 Apache-2.0 许可证；`@types/node` 和 `undici-types` 使用 MIT 许可证。构建依赖不包含在分发的 npm 压缩包中。

CLI 运行时使用 Node.js 内置模块。Node.js、Android SDK Build-Tools、Command-line Tools、SDK Platform 和 JDK 由用户另行安装，适用各自的许可证。npm 包不包含这些工具、真实 APK 或渠道密钥。

## 文档写作技能

仓库内的中文技术写作技能来自 [leter/zh-tech-writing](https://github.com/leter/zh-tech-writing)，采用 MIT 许可证。技能与许可位于 `.agents/skills/zh-tech-writing/`，来源记录在 `skills-lock.json`。这些文件不包含在 CLI 的 npm 包中。

## 渠道协议研究

渠道文档用于研究接口。早期自动查询设计参考过 [apkgo](https://github.com/KevinGong2013/apkgo) 的一般思路，本仓库独立实现协议，没有复制其源码。apkgo 使用 PolyForm Noncommercial 许可证。

渠道 API 和文档仍受各自条款约束，官方入口和验证范围见[渠道接口说明](docs/channels/protocol-evidence.md)。
