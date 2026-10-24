# 第三方声明

项目原创代码采用 [MIT 许可证](LICENSE)。npm 构建依赖的准确版本和完整性摘要记录在 `package-lock.json` 中。TypeScript 使用 Apache-2.0 许可证；`@types/node` 和 `undici-types` 使用 MIT 许可证。构建依赖不包含在交付的 npm tarball 中。

CLI 运行时使用 Node.js 内置模块。Node.js、Android SDK Build Tools 和 JDK 由使用者另行安装，适用各自的许可证。npm tarball 不包含这些工具、真实 APK 或渠道凭据。

仓库内的中文技术写作技能来自 [leter/zh-tech-writing](https://github.com/leter/zh-tech-writing)，采用 MIT 许可证。技能文件和许可保存在 `.agents/skills/zh-tech-writing/`，来源记录在 `skills-lock.json` 中。技能文件不包含在 CLI tarball 内。

渠道文档用于研究协议。早期自动查询设计参考过 [apkgo](https://github.com/KevinGong2013/apkgo) 的一般思路。本仓库独立实现渠道协议，没有复制 apkgo 源码。apkgo 使用 PolyForm Noncommercial 许可证。渠道 API 和文档仍受各自条款约束，来源见[协议参考](docs/channels/protocol-evidence.md)。
