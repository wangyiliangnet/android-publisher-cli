# Mac 安装与分发

支持的分发目标为 Apple Silicon macOS（`darwin-arm64`）。程序名为 `release`，安装包中的 API 命令无需 Go 或 Node 运行环境。APK 验签需要 Android SDK Build Tools 和 JDK。浏览器验收需要固定版本的开发运行环境，但日常生产操作无需先登录浏览器。

## 分发方式

| 安装包 | 用途 | 签名与信任 |
| --- | --- | --- |
| `android-publisher-cli-VERSION-darwin-arm64-ci-preview.tar.gz` | CI 验收与公开测试预览 | ad-hoc 临时签名；未公证；没有稳定的钥匙串信任身份 |
| `android-publisher-cli-VERSION-darwin-arm64.tar.gz` | 本地配置的固定身份分发 | 需明确配置签名身份；公证是单独启用的能力 |

安装前阅读该版本的分发说明。校验和用于发现字节变化，签名验证用于检查包签名；两者都不能使 ad-hoc 预览包成为 Apple 信任的分发包。CI 预览构建不使用开发者证书、不复用本地证书配置，也不含生产凭据。

授予生产钥匙串条目长期访问权限前，应先使用固定签名身份。ad-hoc 身份随构建变化，可能导致 macOS 反复请求授权。正式分发需要团队自己的 Developer ID Application 身份、适当的签名设置及 Apple 公证凭据。本仓库不提供这些材料，也不宣称所有版本均已公证。

## 安装、升级、回滚与卸载

将压缩包及其 `.sha256` 文件下载到同一目录，核对校验和，解压后运行随附的安装程序：

```sh
shasum -a 256 -c android-publisher-cli-VERSION-darwin-arm64-ci-preview.tar.gz.sha256
tar -xzf android-publisher-cli-VERSION-darwin-arm64-ci-preview.tar.gz
cd android-publisher-cli-VERSION-darwin-arm64-ci-preview
/bin/sh install.sh
```

将 `VERSION` 替换为实际版本。默认安装前缀为 `~/.local/android-publisher-cli/`，也可用 `--prefix "/absolute/dedicated/directory"` 指定另一个当前用户所有的目录。请将前缀下的 `bin` 目录加入 PATH。

安装程序检查文件哈希、代码签名、可执行文件架构、所有权和不安全的符号链接，保留各版本目录，并切换 `current` 链接。已安装版本若内容不同，会拒绝安装，需使用新版本号。

升级时，用新安装包的安装程序指定相同前缀。切换可执行文件前，先停止正在执行的发布和查询命令。切回已安装版本时，使用匹配的受管理安装包中的安装程序：

```sh
/bin/sh install.sh --rollback 0.1.0-beta.1
/bin/sh install.sh --uninstall
```

卸载仅删除受管理的命令链接和标记，保留版本文件与发布数据，不删除钥匙串条目。CLI 回滚只切换可执行文件，不降级状态结构，也不绕过恢复或版本检查。状态变化后回滚前，需先检查兼容性。

## 本地构建安装包

在 Apple Silicon macOS 上安装固定版本的 Go，用签名工具配置固定身份，然后运行：

```sh
PUBLISHER_RELEASE_VERSION=0.1.0-beta.1 make package-macos
```

默认打包所需签名身份不可用时会失败，不会静默退回 ad-hoc 签名。仅用于合成数据 CI 预览验收时，可运行：

```sh
PUBLISHER_PACKAGE_MODE=ci-preview \
PUBLISHER_RELEASE_VERSION=0.1.0-beta.1 make package-macos
```

压缩包按明确的允许清单收录可执行文件、安装程序、选定使用文档、构建与版本及分发元数据、校验和、MIT 许可证和依赖声明，不包含源码工作区、APK、运行状态、账号配置、浏览器配置或钥匙串内容。

## 验收与来源

发布工作流重新执行必需检查，并构建两个合成预览版本，验收安装、升级、回滚、卸载、重复安装、字节变化和不安全符号链接，随后生成带有版本、提交信息及校验和的草稿。维护者审阅后手工发布；该流程不向应用商店提审。

再次分发安装包时保留第三方声明。依赖保留各自的许可证，项目的 MIT 许可证不会替代这些条款。来源和公开发布条件见[发布流程](release-process.md)。
