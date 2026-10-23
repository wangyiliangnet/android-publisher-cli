# 维护者：构建、签名与分发

本页说明如何给团队制作 CLI 安装包。安装包使用者的操作步骤见[首次使用](colleague-quickstart.md)。创建 GitHub 标签和发布草稿见[版本发布](release-process.md)。

## 选择分发方式

先确认这次是做测试预览，还是给同事长期使用：

| 安装包 | 用途 | 签名与信任 |
| --- | --- | --- |
| `android-publisher-cli-VERSION-darwin-arm64-ci-preview.tar.gz` | 合成数据 CI 验收与公开测试预览 | ad-hoc 临时签名；未公证；没有稳定的钥匙串信任身份 |
| `android-publisher-cli-VERSION-darwin-arm64.tar.gz` | 使用本机配置的固定签名身份分发 | 必须明确配置签名身份；公证单独启用 |

同事需要长期访问生产钥匙串条目时，应采用固定签名身份，并在目标 Mac 上验证。ad-hoc 身份随构建变化，可能导致反复授权。CI 预览不使用开发者证书，不读取本地签名配置，也不包含生产凭据。

正式分发前，另行准备团队 Developer ID Application 身份、签名设置和 Apple 公证凭据。本仓库不提供这些材料。证书签名与公证是两个步骤。分发状态以包内 `DISTRIBUTION.txt` 和实际验证结果为准。

## 构建安装包

### 1. 准备构建环境和版本号

在 Apple Silicon macOS 上安装仓库固定版本的 Go。版本号示例使用 `0.1.0-beta.1`，执行时换成本次版本。产物内容不同时，必须使用新版本号。不能重建后替换同版本文件。

### 2. 按用途构建

制作固定签名包前，用本机签名工具配置身份。构建脚本从以下文件读取证书的 40 位 SHA-1 指纹：

```text
~/Library/Application Support/AndroidPublisherCLI/Signing/identity.sha1
```

身份文件必须是归当前用户所有的普通文件，权限为 `0400` 或 `0600`。也可用 `PUBLISHER_SIGNING_IDENTITY_FILE` 指定其他身份文件。本机签名工具必须能够使用对应证书。配置完成后，运行：

```sh
PUBLISHER_RELEASE_VERSION=0.1.0-beta.1 make package-macos
```

身份不可用时，打包会失败，不会改用 ad-hoc 签名。需要 Developer ID 签名时，明确设置 `PUBLISHER_DEVELOPER_ID=1`。需要公证时，再设置 `PUBLISHER_NOTARY_PROFILE`，指定已配置的钥匙串公证配置名。公证要求 Developer ID Application 和 hardened runtime 签名。

仅做合成数据预览验收时，使用：

```sh
PUBLISHER_PACKAGE_MODE=ci-preview \
PUBLISHER_RELEASE_VERSION=0.1.0-beta.1 make package-macos
```

CI 预览会拒绝 Developer ID 和公证选项。正式签名和公证需独立配置。不能向 CI 加入渠道生产凭据。

### 3. 检查生成的文件

打包成功后，终端会输出 `Bundle:` 和压缩包路径。`dist/` 中还会生成同名 `.sha256` 文件。按本次包的文件名校验，例如：

```sh
cd dist
shasum -a 256 -c android-publisher-cli-0.1.0-beta.1-darwin-arm64-ci-preview.tar.gz.sha256
```

确认对应文件为 `OK`。固定签名包使用没有 `-ci-preview` 后缀的文件名。继续交付前，完成下面的实际包检查。

## 随包文档与检查

### 4. 核对安装包内容

解压实际生成的安装包，逐项确认：

- 可执行文件的签名、架构、版本和提交与本次构建一致。
- 校验和、构建元数据、`DISTRIBUTION.txt` 与实际产物一致。
- 文件只来自允许清单：程序、安装程序、文档、构建和分发元数据、校验和、MIT 许可证及依赖声明。
- 包中没有源码工作区、APK、运行状态、账号配置、浏览器配置或钥匙串内容。
- 离线文档链接可以打开，第三方声明完整。

安装包 README 只提供离线导航。首次使用、日常发布和状态解释供操作者使用。构建分发、协议参考和版本发布供维护者查阅。再次分发时，保留第三方声明。项目 MIT 许可证不替代依赖许可。

### 5. 验收安装和版本切换

准备两个不同版本的包及各自的校验文件。在仓库根目录运行现有验收脚本。第一个参数是新包，第二个参数是旧包。请将示例路径换成实际路径：

```sh
/bin/sh tools/smoke-package-macos.sh \
  dist/android-publisher-cli-0.1.0-beta.2-darwin-arm64-ci-preview.tar.gz \
  dist/android-publisher-cli-0.1.0-beta.1-darwin-arm64-ci-preview.tar.gz
```

脚本会检查离线链接，并在临时目录验收安装、升级、回滚、重复安装、卸载，以及同版本内容冲突、篡改和符号链接拒绝行为。

通过这些检查后，按[版本发布](release-process.md)复核草稿或处理上传中断。检查通过只证明对应测试成功。真实账号权限和 Apple 信任仍需分别验证。
