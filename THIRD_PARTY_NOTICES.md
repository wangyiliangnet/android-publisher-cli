# 第三方声明

本页记录代码、依赖和接口研究资料的来源及许可，供维护者复用代码和分发安装包前核对。项目原创代码采用 [MIT 许可证](LICENSE)。

## 构建与运行工具

npm 构建依赖的准确版本和完整性校验值记录在 `package-lock.json`。TypeScript 使用 Apache-2.0 许可证；`@types/node` 和 `undici-types` 使用 MIT 许可证。构建依赖不包含在分发的 npm 压缩包中。

CLI 使用 Node.js 内置模块。Node.js 由用户安装。开发验收生成合成 APK 时使用 Android SDK 与 JDK，日常发布使用工具管理的检查组件。

检查组件固定为官方 `com.android.tools.build:apksig:8.13.0` 和 Eclipse Temurin 21.0.13+9 的 macOS AArch64 JRE。下载 URL 与 SHA256 固定在 `src/runtime.ts`。apksig 的 [Maven 许可声明](https://dl.google.com/dl/android/maven2/com/android/tools/build/apksig/8.13.0/apksig-8.13.0.pom) 为 Apache-2.0，[源码包](https://dl.google.com/dl/android/maven2/com/android/tools/build/apksig/8.13.0/apksig-8.13.0-sources.jar) 由 Google 发布。Temurin 按 [GPLv2 with Classpath Exception](https://adoptium.net/docs/faq) 分发，安装保留原始归档中的许可证与法律声明；对应发布与源代码入口见 [Temurin 21 发布](https://github.com/adoptium/temurin21-binaries/releases/tag/jdk-21.0.13%2B9)。

工具独立编写的 Java 入口位于 `tools/ApkVerifierMain.java`，预编译字节码以文本随 npm 包分发。macOS 凭据功能调用系统自带的 `security`，加密材料的密钥保存在钥匙串。Web 的样式和图标由本项目实现，没有复制 GitHub 前端源码或引入在线资源。npm 包不包含真实 APK、渠道密钥或下载后的运行时。

## 文档写作技能

仓库内的中文技术写作技能来自 [leter/zh-tech-writing](https://github.com/leter/zh-tech-writing)，采用 MIT 许可证。技能与许可位于 `.agents/skills/zh-tech-writing/`，来源记录在 `skills-lock.json`。这些文件不包含在 CLI 的 npm 包中。

## 渠道协议研究

渠道文档用于研究接口。早期自动查询设计参考过 [apkgo](https://github.com/KevinGong2013/apkgo) 的一般思路，本仓库独立实现协议，没有复制其源码。apkgo 使用 PolyForm Noncommercial 许可证。

渠道 API 和文档仍受各自条款约束，官方入口和验证范围见[渠道接口说明](docs/channels/protocol-evidence.md)。
