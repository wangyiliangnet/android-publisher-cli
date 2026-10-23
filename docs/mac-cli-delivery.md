# 维护者：构建、签名与分发

面向为团队制作 CLI 安装包的维护者。安装、升级、回滚和卸载步骤统一见[首次使用](colleague-quickstart.md)；GitHub 标签与草稿操作见[版本发布](release-process.md)。

## 选择分发方式

| 安装包 | 用途 | 签名与信任 |
| --- | --- | --- |
| `android-publisher-cli-VERSION-darwin-arm64-ci-preview.tar.gz` | 合成数据 CI 验收与公开测试预览 | ad-hoc 临时签名；未公证；没有稳定的钥匙串信任身份 |
| `android-publisher-cli-VERSION-darwin-arm64.tar.gz` | 本地配置的固定身份分发 | 需明确配置签名身份；公证单独启用 |

CI 预览不使用开发者证书、不复用本地签名配置，也不含生产凭据。让同事长期访问生产钥匙串条目前，应采用固定签名身份并在目标 Mac 上验证；ad-hoc 身份随构建变化，可能导致反复授权。

正式分发需要团队的 Developer ID Application 身份、相应签名设置和 Apple 公证凭据，本仓库不提供这些材料。固定证书签名也不自动等于已公证，以每个产物的 `DISTRIBUTION.txt` 和实际验证结果为准。

## 构建安装包

在 Apple Silicon macOS 上安装仓库固定版本的 Go，使用签名工具配置固定身份，再运行：

```sh
PUBLISHER_RELEASE_VERSION=0.1.0-beta.1 make package-macos
```

默认打包所需身份不可用时会失败，不会退回 ad-hoc。仅做合成数据预览验收时使用：

```sh
PUBLISHER_PACKAGE_MODE=ci-preview \
PUBLISHER_RELEASE_VERSION=0.1.0-beta.1 make package-macos
```

版本号只是示例，每份不同产物必须使用新版本。CI 预览拒绝 Developer ID 和公证选项；正式签名与公证需独立、明确地配置，不能向 CI 加入渠道生产凭据。

## 随包文档与检查

安装包 README 只提供离线导航。首次使用、日常发布和状态解释面向操作者；构建分发、协议参考及版本发布保留为维护者参考，不重复放入首页操作步骤。

打包按明确允许清单收录可执行文件、安装程序、文档、构建与版本及分发元数据、校验和、MIT 许可证和依赖声明，不含源码工作区、APK、运行状态、账号配置、浏览器配置或钥匙串内容。再次分发时保留第三方声明，项目 MIT 许可证不替代依赖许可。

交付前检查实际包的签名、架构、版本与提交、校验和、允许清单和离线链接，并完成双版本安装、升级、回滚、卸载及篡改和符号链接拒绝验收。工作流通过只证明这些检查，账号权限和 Apple 信任需按各自条件验证。草稿复核与中断恢复见[版本发布](release-process.md)。
