# 安装与配置

你需要 Apple Silicon Mac、[`.node-version`](../.node-version) 指定的 Node.js、Android SDK Build Tools 和 JDK。配置应用时，还需要一份可信的已签名、不可调试 APK。

## 安装 CLI

```sh
npm install --global android-publisher-cli
android-release version --json
android-release doctor
```

`version` 应显示安装版本。`doctor` 返回的 `ready` 为 `true`，表示 CLI 找到了 APK 检查工具。如果工具未找到，检查 `ANDROID_SDK_ROOT` 和 `JAVA_HOME` 是否指向本机安装目录，再运行 `doctor`。

需要安装预发布版本时，使用 `npm install --global android-publisher-cli@next`。维护者交付的 tarball 安装方法见 [npm 包验收](npm-package.md)。

## 配置应用

`setup` 从可信 APK 读取包名和签名，不上传文件。运行向导：

```sh
android-release setup --project demo --apk "/absolute/path/trusted.apk"
```

核对显示的包名和签名 SHA-256，再输入 `yes`。选择渠道并填写公开账号和应用标识后，再输入一次 `yes` 保存项目。输入 `cancel` 可退出。项目保存在 `~/Library/Application Support/AndroidPublisherCLI/v2/projects/`。

| 渠道 | 向导需要的公开信息 | 执行时输入的凭据 |
| --- | --- | --- |
| 华为 | 本机账号名、渠道应用 ID，可选中文语言 ID | 服务账号 JSON |
| 荣耀 | 本机账号名、渠道应用 ID、`client_id`，可选中文语言 ID | `client_secret` |
| 小米 | 本机账号名、`userName`、图标路径、隐私政策 URL；应用 ID 使用包名 | API 密钥、公钥证书和按需提供的测试账号 JSON |
| OPPO | `client_id` 兼作本机账号名；应用 ID 使用包名 | `client_secret` |
| vivo | 本机账号名、`access_key`；应用 ID 使用包名 | `access_secret` |
| 应用宝 | 本机账号名、应用 AppID、账号 uid | API 密钥 |

公开标识须与渠道后台一致。`setup` 不验证渠道权限；运行[预检](release-workflow.md#预检)后才能看到账号和应用的读取结果。需要重设项目时，加 `--replace` 并重新核对可信 APK。已有批次保留创建时冻结的配置。

已有项目 JSON 时，可以导入文件：

```sh
android-release setup --file /absolute/path/project.json \
  --apk /absolute/path/trusted.apk
```

CLI 会核对配置中的包名、签名与 APK 是否一致。

## 输入凭据

`batch preflight`、`batch submit` 和刷新查询会在终端逐项提示凭据。CLI 隐藏输入，不把渠道密钥写入项目配置。华为服务账号 JSON 和小米公钥证书可以放在当前用户私有文件中。在提示处输入 `@` 加绝对路径，例如 `@/absolute/path/service-account.json`。

```sh
chmod 600 "/absolute/path/service-account.json"
```

自动化可传入 `--credentials-file /absolute/path/credentials.json`。文件须属于当前用户，权限为 `0600`。顶层按渠道名分组；字段名见 CLI 提示。将文件放在仓库外，并自行管理备份和轮换。

CLI 不读取 macOS 钥匙串。短期访问令牌和上传授权保存在本机私有状态库。真实账号、私钥和凭据文件不能提交到 Git，也不能附在 issue 或报告里。

## 更新与卸载

停止正在运行的发布命令，再安装指定版本的 npm 包或 tarball。运行 `android-release version --json` 核对版本。回退前，确认旧程序能读取当前 `v2` 状态结构。

```sh
npm uninstall --global android-publisher-cli
```

卸载 npm 包不会删除本机发布状态或凭据文件。配置完成后，按[发布应用更新](release-workflow.md)建立批次。
