# 安装与环境检查

在 Apple Silicon Mac 上安装 Node.js 24.22.0、npm 11.19.1 和 CLI。APK 已由你自己的构建流程打包并签名，发布时无需配置 Android 构建环境。

```sh
node --version
npm --version
npm install --global android-publisher-cli
android-release version
```

版本要求也记录在 [`.node-version`](../.node-version)。从 `.tgz` 安装和验证，见[npm 安装包验收](npm-package.md)。

## APK 检查组件

首次运行 `publish` 时，工具自动下载固定版本的 Java 运行时和官方 `apksig`，验证 SHA256 后安装到本机私有目录。安装后，准备材料和生成提交计划直接使用本机组件，保持离线。

可以提前安装或修复组件：

```sh
android-release doctor --repair
android-release doctor
android-release doctor --json
```

普通 `doctor` 只读取本机状态。`--repair` 需要联网下载检查组件。下载失败或完整性校验不通过时停止安装，修复网络后重试。工具使用环境中的 HTTP/HTTPS 代理设置，遵循 `NO_PROXY`。

检查通过时退出码为 `0`。缺少组件或平台不支持时为 `1`，输出给出修复入口。发布时保持完整签名验证；组件故障需要先修复。

## 首次配置与本地工作台

```sh
android-release app add demo
android-release web
```

首次配置填写应用和渠道资料，无需选择 APK。浏览器工作台与 CLI 共用本机记录。服务仅监听本机地址；在终端输入凭据后，Web 即可复用。

更新和卸载仍使用 npm。卸载包会保留本机应用、发布记录、加密凭据和检查组件。渠道密钥可先用 `account forget` 清除。

## 贡献者生成测试 APK

维护者的真实 APK 测试会临时构建并签名合成应用。这类测试需要 Android SDK Build-Tools、Command-line Tools、SDK Platform 和 JDK。

开发环境使用 `ANDROID_SDK_ROOT` 和 `JAVA_HOME`。SDK 下需要 `aapt2`、`apksigner`、`apkanalyzer` 和 `android.jar`；JDK 下需要 `java` 和 `keytool`。CI 在隔离目录安装这些工具。

```sh
PUBLISHER_REQUIRE_ANDROID_TESTS=1 npm run check
```

这些依赖用于开发验收，日常发布使用 CLI 管理的检查组件。
