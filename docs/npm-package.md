# npm 包验收

本页供检查 npm 交付物的维护者使用。包内包含编译后的 JavaScript 和离线文档。运行时需要 [`.node-version`](../.node-version) 指定的 Node.js，以及单独安装的 Android SDK Build Tools 和 JDK。

## 构建与检查

在仓库根目录安装锁定依赖，并运行完整检查：

```sh
npm ci --ignore-scripts
PUBLISHER_REQUIRE_ANDROID_TESTS=1 npm run check
npm pack --dry-run --json
```

`npm run check` 执行类型检查、构建和测试。最后一条命令列出包文件。确认清单包含 `build/`、`docs/`、`README.md`、许可证和 `package.json`，且没有凭据、APK、状态库、私钥或本机路径。npm 包没有安装脚本。

## 安装实际 tarball

```sh
mkdir -p dist
archive=$(npm pack --pack-destination dist --silent)
cd dist
shasum -a 256 "$archive" > "$archive.sha256"
shasum -a 256 -c "$archive.sha256"
prefix=$(mktemp -d)
npm install --global --prefix "$prefix" --ignore-scripts "./$archive"
"$prefix/bin/android-release" version --json
```

摘要检查应显示 `OK`。`version` 应与 tarball 文件名和 `package.json` 一致。需要核对 Android 工具时，再运行 `"$prefix/bin/android-release" doctor`。CI 会用独立前缀安装已检查的 tarball。

## 分发约束

Node.js 和 Android 工具由使用者分别安装，并遵守各自许可。包内包含 MIT 许可证和[第三方声明](../THIRD_PARTY_NOTICES.md)。真实渠道凭据和 APK 不能进入 npm 包、CI 或 GitHub 发布附件。

一个包名和版本只能在 npm 注册表发布一次。内容改变后，先提升版本，再生成新包。版本 PR、标签和发布顺序见 [CLI 版本发布](npm-release.md)。
