# 首次使用

本页供第一次安装和配置 CLI 的发版同事使用。你需要 Apple Silicon Mac、Android SDK Build Tools、JDK，以及 [`.node-version`](../.node-version) 指定的 Node.js。使用 npm 安装包时不需要 Go。

## 安装 CLI

从维护者取得已验收的 npm tarball 和 SHA-256 文件。先核对摘要，再安装对应文件。下面的版本号仅作示例：

```sh
shasum -a 256 -c android-publisher-cli-0.2.0-dev.tgz.sha256
npm install --global ./android-publisher-cli-0.2.0-dev.tgz
release version --json
release doctor
```

校验输出应为 `OK`。`version` 应显示你安装的版本。`doctor` 的 `ready` 为 `true` 时，表示已找到 APK 检查工具。若工具未找到，先检查 SDK 和 JDK 路径，再使用 `ANDROID_SDK_ROOT` 与 `JAVA_HOME` 指向本机安装目录。

从源码试用时，在仓库根目录执行 `npm ci --ignore-scripts`、`npm run build` 和 `npm link`。正式交付与核对步骤见[构建与分发](mac-cli-delivery.md)。

## 配置应用

准备一份你信任的已签名、不可调试 APK。下面的命令读取包名和签名，并打开项目向导；它不会上传 APK：

```sh
release setup --project demo --apk "/absolute/path/trusted.apk"
```

先核对显示的包名与签名 SHA-256。输入 `yes` 后选择渠道，填写公开账号和应用标识。最后再次输入 `yes` 保存项目。输入 `cancel` 可退出向导。项目保存在 `~/Library/Application Support/AndroidPublisherCLI/v2/projects/`。

| 渠道 | 向导需要的公开信息 | 执行命令时输入的凭据 |
| --- | --- | --- |
| 华为 | 本机账号名、渠道应用 ID，可选中文语言 ID | 服务账号 JSON |
| 荣耀 | 本机账号名、渠道应用 ID、`client_id`，可选中文语言 ID | `client_secret` |
| 小米 | 本机账号名、`userName`、图标路径、隐私政策 URL；应用 ID 使用包名 | API 密钥、公钥证书和按需提供的测试账号 JSON |
| OPPO | `client_id` 兼作本机账号名；应用 ID 使用包名 | `client_secret` |
| vivo | 本机账号名、`access_key`；应用 ID 使用包名 | `access_secret` |
| 应用宝 | 本机账号名、应用 AppID、账号 uid | API 密钥 |

账号和应用标识要与渠道后台一致。配置保存的是公开标识，渠道权限还需通过预检确认。需要重设项目时，加 `--replace` 并重新核对可信 APK。已准备的旧批次不会随项目配置改变。

## 输入凭据

`batch preflight`、`batch submit` 和刷新查询时，CLI 默认在终端逐项提示凭据。输入不会显示在屏幕上，也不会写入项目配置。华为服务账号 JSON 和小米公钥证书可先放在当前用户私有文件里。在提示处输入 `@` 加绝对路径，例如 `@/absolute/path/service-account.json`。

```sh
chmod 600 "/absolute/path/service-account.json"
```

自动化可使用 `--credentials-file /absolute/path/credentials.json`。文件须属于当前用户，权限为 `0600`。顶层按渠道名分组；每组字段名见 CLI 提示。请把文件放在仓库外，并自行管理它的备份和轮换。

CLI 不读取钥匙串。短期访问令牌和上传授权会保存在本机私有状态库。真实账号、私钥和凭据文件不能提交到 Git，也不能附在 issue 或报告里。

## 升级与卸载

停止正在运行的发布命令，再安装新版 tarball。运行 `release version --json` 核对版本。回退时，重新安装你保留的旧版 tarball；先确认旧程序能读取当前 `v2` 状态结构。

```sh
npm uninstall --global android-publisher-cli
```

卸载 npm 包不会删除发布状态或凭据文件。0.1.x Go 版状态不会自动迁移到新版。发布步骤见[日常发布](daily-cli-workflow.md)。
