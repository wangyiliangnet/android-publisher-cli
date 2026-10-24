# 安装与配置

你需要 Apple Silicon Mac 和 [`.node-version`](../.node-version) 指定的 Node.js。运行 `prepare` 前，还需安装 Android SDK Build Tools 和 JDK，并准备已签名、不可调试的 APK。

## 安装 CLI

```sh
npm install --global android-publisher-cli
android-release version --json
android-release doctor
```

`version` 应显示安装版本。`doctor` 返回的 `ready` 为 `true`，表示 CLI 找到了 APK 检查工具。如果工具未找到，检查 `ANDROID_SDK_ROOT` 和 `JAVA_HOME` 是否指向本机安装目录，再运行 `doctor`。

需要安装预发布版本时，使用 `npm install --global android-publisher-cli@next`。维护者交付的 tarball 安装方法见 [npm 包验收](npm-package.md)。

## 配置应用

`setup` 只保存渠道公开信息，不读取 APK，也不访问渠道。运行向导：

```sh
android-release setup --project demo
```

选择渠道，填写公开账号和应用标识。核对项目摘要后，输入 `yes` 保存；输入 `cancel` 可退出。项目保存在 `~/Library/Application Support/AndroidPublisherCLI/v2/projects/`。

| 渠道 | 向导需要的公开信息 | 执行时输入的凭据 |
| --- | --- | --- |
| 华为 | 本机账号名、渠道应用 ID，可选中文语言 ID | 服务账号 JSON 文件路径 |
| 荣耀 | 本机账号名、渠道应用 ID、`client_id`，可选中文语言 ID | 直接输入 `client_secret` |
| 小米 | 本机账号名、`userName`、图标路径、隐私政策 URL；应用 ID 使用包名 | 直接输入 API 密钥；输入公钥证书和可选测试账号 JSON 的文件路径 |
| OPPO | `client_id` 兼作本机账号名；应用 ID 使用包名 | 直接输入 `client_secret` |
| vivo | 本机账号名、`access_key`；应用 ID 使用包名 | 直接输入 `access_secret` |
| 应用宝 | 本机账号名、应用 AppID、账号 uid | 直接输入 API 密钥 |

公开标识须与渠道后台一致。小米、OPPO 和 vivo 的应用 ID 填目标包名；`prepare` 会与 APK 中的包名核对。

`setup` 不验证渠道权限。[预检](release-workflow.md#预检)才会读取账号和应用状态。需要重设项目时，加 `--replace`；已有批次保留创建时冻结的配置。

## 输入凭据

预检、提交、继续执行、刷新查询和核对时，CLI 会在终端逐项提示凭据。短文本直接输入，屏幕不显示输入内容。

华为服务账号 JSON、小米公钥证书和可选测试账号 JSON 须放在当前用户私有文件中。在对应提示处输入文件绝对路径。

```sh
chmod 600 "/absolute/path/service-account.json"
```

凭据文件须属于当前用户，权限为 `0600`。路径须为规范化绝对路径，不能经过符号链接。将文件放在仓库外，自行管理备份和轮换。命令执行时需要可交互的终端。

CLI 不读取 macOS 钥匙串。短期访问令牌和上传授权保存在本机私有状态库。真实账号、私钥和凭据文件不能提交到 Git，也不能附在 issue 或报告里。

## 更新与卸载

停止正在运行的发布命令，再安装指定版本的 npm 包或 tarball。运行 `android-release version --json` 核对版本。回退前，确认旧程序能读取当前 `v2` 状态结构。

```sh
npm uninstall --global android-publisher-cli
```

卸载 npm 包不会删除本机发布状态或凭据文件。配置完成后，按[发布应用更新](release-workflow.md)建立批次。
