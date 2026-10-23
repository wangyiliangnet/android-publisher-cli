# 第三方声明

项目源码采用 LICENSE 中的 MIT 许可证。依赖保留各自的版权声明和许可证。

Go 依赖及其准确版本记录在 go.mod 和 go.sum 中。打包工具会收集已安装依赖的许可证及声明文件，连同 Go 标准库许可证一起写入每份二进制压缩包的 THIRD_PARTY_LICENSES.txt。找不到某个模块的声明文件时，打包会失败。

可选的浏览器观测器使用 browser/package-lock.json 固定的依赖，其声明可在安装后的 npm 包中查看。CLI 二进制压缩包不包含 Node.js、Playwright 浏览器及其第三方组件；这些运行环境需另行安装，并遵守各自附带的声明。

渠道文档和 apkgo 项目用于研究协议。本仓库独立实现协议，不包含 apkgo 源码；项目的 MIT 许可证不适用于这些外部来源。来源引用和适用范围见 docs/channels/protocol-evidence.md。
