# 维护者：npm 构建与分发

本页供制作 CLI 安装包的维护者使用。新版交付物是 npm tarball，包含编译后的 JavaScript 和离线文档。使用者需要安装 [`.node-version`](../.node-version) 指定的 Node.js；tarball 不包含 Node.js、Android SDK 或 JDK。

## 构建并验收

在仓库根目录安装固定依赖，并运行完整检查：

```sh
npm ci --ignore-scripts
PUBLISHER_REQUIRE_ANDROID_TESTS=1 npm run check
npm pack --dry-run --json
```

最后一条命令列出实际包文件。应看到 `build/`、文档、许可证和 `package.json`。检查列表里没有凭据、APK、状态库、测试私钥或本机路径。npm 包不会运行安装脚本。

生成 tarball，并保存对应摘要。下面以 `0.2.0-dev` 为例：

```sh
mkdir -p dist
npm pack --pack-destination dist
cd dist
shasum -a 256 android-publisher-cli-0.2.0-dev.tgz \
  > android-publisher-cli-0.2.0-dev.tgz.sha256
shasum -a 256 -c android-publisher-cli-0.2.0-dev.tgz.sha256
```

核对输出为 `OK`。安装实际 tarball，再运行 `release version --json` 和 `release doctor`。CI 也会在独立前缀安装已检查的 tarball，确认命令可运行。

## 分发边界

tarball 没有原生可执行文件，因此旧版 macOS `codesign`、公证和安装脚本不再适用。包内包含 MIT 许可证和[第三方声明](../THIRD_PARTY_NOTICES.md)。Node.js 与 Android 工具由使用者分别安装，并遵守各自许可。

仓库目前把 `package.json` 标为 `private`。我们通过文件交付 tarball，没有向 npm 注册表发布。真实渠道凭据和 APK 不能进入 npm 包、CI 或 GitHub 发布附件。

同一版本号对应固定字节。内容改变时，先更新版本号与 `package-lock.json`，再生成新包。版本提交、标签和草稿的顺序见[版本发布](release-process.md)。
