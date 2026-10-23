# 第三方声明

项目源码采用 LICENSE 中的 MIT 许可证。依赖保留各自的版权声明和许可证。

仓库中的中文技术写作技能来自 [leter/zh-tech-writing](https://github.com/leter/zh-tech-writing)，采用 MIT 许可证。技能文件及参考资料保存在 .agents/skills/zh-tech-writing/，该目录的 LICENSE 保留上游版权和许可声明。来源与内容哈希记录在 skills-lock.json 中。这些文件不包含在 CLI 安装包中。

Go 依赖及其准确版本记录在 go.mod 和 go.sum 中。打包工具会收集已安装依赖的许可证及声明文件，以及 Go 标准库许可证。这些内容写入每份二进制压缩包的 THIRD_PARTY_LICENSES.txt。找不到某个模块的声明文件时，打包会失败。

可选的浏览器观测器使用 browser/package-lock.json 固定的依赖。依赖声明可在安装后的 npm 包中查看。CLI 二进制压缩包不包含 Node.js、Playwright 浏览器及其第三方组件。这些运行环境需另行安装，并遵守各自附带的声明。

渠道文档用于协议研究。自动查询流程参考了 [apkgo](https://github.com/KevinGong2013/apkgo) 的一般思路。本仓库独立实现协议和解析器，不包含 apkgo 源码。apkgo 采用 PolyForm Noncommercial 许可证。未经另行授权，不得复制或翻译其源码并按本项目 MIT 许可证使用。渠道文档和服务仍适用各自条款。具体来源见随包或仓库中的 docs/channels/protocol-evidence.md。
