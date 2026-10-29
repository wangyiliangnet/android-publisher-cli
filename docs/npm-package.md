# npm 安装包验收

本页面向维护者，用于验证用户实际拿到的安装包能否安装和运行。npm 安装包是 `.tgz` 压缩包，包含编译后的 JavaScript、文档和许可证；测试源码构建成功后，还要安装同一份压缩包验证命令入口。

普通用户直接按[安装说明](getting-started.md)使用 npm。CLI 新版本的发布步骤见[发布 CLI 版本](npm-release.md)。

## 构建并检查包内文件

在仓库根目录，使用 [`.node-version`](../.node-version) 指定的 Node.js 和项目要求的 npm 版本。完整验收还需要 [Android SDK 和 JDK](getting-started.md#安装-android-工具)。

```sh
npm ci --ignore-scripts
PUBLISHER_REQUIRE_ANDROID_TESTS=1 npm run check
npm pack --dry-run --json
```

前两步安装锁定依赖并完成类型检查、构建和测试。最后一条命令只列出将打包的文件，不生成压缩包。清单应包含 `build/`、`docs/`、`README.md`、许可证和 `package.json`，不含密钥、APK、状态库、私钥或个人本机路径。npm 包没有安装脚本。

## 安装实际压缩包

下面在 `dist/` 生成压缩包并计算文件校验值，再安装到临时目录，避免覆盖本机正在使用的 CLI：

```sh
mkdir -p dist
archive=$(npm pack --pack-destination dist --silent)
cd dist
shasum -a 256 "$archive" > "$archive.sha256"
shasum -a 256 -c "$archive.sha256"
prefix=$(mktemp -d)
npm install --global --prefix "$prefix" --ignore-scripts "./$archive"
"$prefix/bin/android-release" version --json
"$prefix/bin/android-release" --help
```

校验应显示 `OK`。版本应与压缩包文件名和 `package.json` 一致，帮助应正常列出命令。CI 也会用独立目录安装已检查的压缩包。

已配置 Android 工具时，可再运行 `"$prefix/bin/android-release" doctor`。诊断失败时按提示修复本机环境；脚本解析结果须使用 `doctor --json`。Android SDK Build-Tools、Command-line Tools、SDK Platform 和 JDK 由用户另行安装，不包含在 npm 包中。

## 交付与版本约束

保留同一份通过验证的压缩包及校验文件。许可证和来源见[第三方声明](../THIRD_PARTY_NOTICES.md)。真实渠道密钥和 APK 不能进入 npm 包、CI 或 GitHub 发布附件。

npm 上同一包名的同一版本只能发布一次。内容改变后须使用新版本号，再构建、验收和发布，不能覆盖旧版本。
