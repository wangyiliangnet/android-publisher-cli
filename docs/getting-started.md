# 安装与环境检查

完成本页后，你应能运行 `android-release`，并用本机 Android 工具检查 APK。连接商店账号在下一步[配置应用与渠道账号](configuration.md)中完成。

## 安装运行环境和 CLI

当前版本使用 Apple Silicon Mac、Node.js 24.22.0 和 npm 11.19.1。先在终端核对版本：

```sh
node --version
npm --version
```

Node.js 版本也记录在仓库的 [`.node-version`](../.node-version) 中。若命令不存在或版本不符，先安装对应版本，再安装 CLI：

```sh
npm install --global android-publisher-cli
android-release version
```

最后一条命令应显示已安装的 CLI 版本。需要指定版本时，在包名后加版本号，例如 `android-publisher-cli@0.3.0`；预发布版本使用 `android-publisher-cli@next`。从维护者提供的 `.tgz` 文件安装，见 [npm 安装包验收](npm-package.md#安装实际压缩包)。

## 安装 Android 工具

CLI 用 Android SDK 验证 APK 签名、读取包名和版本，用 JDK 运行其中的 Java 工具。已有 Android Studio 也可能缺少某个 SDK 组件。

打开 Android Studio → **Tools → SDK Manager**，确认 **Android SDK Location**，再安装以下组件。安装方式见 [Android 官方说明](https://developer.android.com/studio/intro/update#sdk-manager)。

| 位置 | 需要安装的组件 | 用途 |
| --- | --- | --- |
| SDK Tools | Android SDK Build-Tools | 提供 `apksigner` 签名检查工具和 `aapt2` 资源工具 |
| SDK Tools | Android SDK Command-line Tools (latest) | 提供 `apkanalyzer`，读取 APK 包名和版本 |
| SDK Platforms | 一个 Android SDK Platform，例如 Android 16 / API 36 | 提供 `android.jar` 平台文件 |

点击 **Apply** 完成安装。JDK 可使用 Android Studio 自带的版本；本项目以 JDK 21 验证。JDK 目录下应能找到 `bin/java`。

## 运行检查并修复问题

```sh
android-release doctor
```

命令会逐项显示“通过”“缺失”或“失败”，并给出修复步骤。它会实际启动 Java、`apksigner`、`apkanalyzer` 和 `aapt2`，检查 `android.jar` 是否可读。所有项目通过才会显示“环境检查通过”，退出码为 `0`。检查只访问本机，不登录渠道或提交应用。

例如，`APK 信息读取工具（apkanalyzer）` 提示缺失时，打开 SDK Manager 的 SDK Tools。安装 **Android SDK Command-line Tools (latest)** 后，文件应位于：

```text
<SDK 目录>/cmdline-tools/latest/bin/apkanalyzer
```

版本号目录也支持，例如 `cmdline-tools/22.0/bin/apkanalyzer`。手动解压时，`bin` 和 `lib` 要放在同一个版本目录下。`apkanalyzer` 所属组件和目录见[官方说明](https://developer.android.com/tools/apkanalyzer)。

如果组件已安装却仍提示缺失，核对 `doctor` 显示的 SDK 目录是否与 SDK Manager 一致。默认使用 `~/Library/Android/sdk`；自定义位置可通过参数检查：

```sh
android-release doctor \
  --sdk-root "/absolute/path/android-sdk" \
  --java-home "/absolute/path/jdk/Contents/Home"
```

这两个参数只对本次命令生效。确定路径后，在当前终端设置环境变量，后续的 APK 检查、准备和提交命令就会使用相同路径：

```sh
export ANDROID_SDK_ROOT="/absolute/path/android-sdk"
export JAVA_HOME="/absolute/path/jdk/Contents/Home"
android-release doctor
```

把示例路径替换为本机目录。Android Studio 自带 JDK 的常见位置是 `/Applications/Android Studio.app/Contents/jbr/Contents/Home`。需要新终端继续使用这些设置时，将两条 `export` 写入 `~/.zshrc`。

SDK 目录的选择顺序为 `--sdk-root`、`ANDROID_SDK_ROOT`、`ANDROID_HOME`、默认目录。JDK 的选择顺序为 `--java-home`、`JAVA_HOME`、Android Studio 自带 JDK 的常见位置。

工具文件存在但启动失败时，按输出检查 JDK 兼容性、执行权限和安装完整性。损坏的 SDK 组件可在 SDK Manager 中重新安装，修复后再次运行 `doctor`。

脚本需要结构化诊断时，使用 `android-release doctor --json`，读取 `ready` 和各项 `checks`。旧脚本若直接解析 `doctor` 输出，也需加上 `--json`。

## 更新或卸载

停止正在运行的发布命令后，安装新版本，再用 `android-release version` 核对。回退旧版本前，确认旧版本能读取当前的 `v2` 发布记录。

安装指定版本，将 `VERSION` 换成实际版本号：

```sh
npm install --global android-publisher-cli@VERSION
```

需要卸载时运行：

```sh
npm uninstall --global android-publisher-cli
```

卸载 npm 包会保留本机发布记录。安装完成后，继续[配置应用与渠道账号](configuration.md)。
