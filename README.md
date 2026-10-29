# Android 发布工具

`android-release` 把已签名的渠道 APK 提交到华为、荣耀、小米、OPPO、vivo 和应用宝中已有的应用，并追踪每个渠道的结果。面向在 Apple Silicon Mac 上发版的开发者和发布负责人，提供终端和本地 Web 两种入口。

首次保存应用资料和渠道账号。每次发版为各渠道明确指定 APK，提供一份更新说明。工具自动检查、展示提交摘要，确认一次后提交。默认选择应用已配置的全部渠道，审核通过后立即上架。

## 安装与开始使用

准备 Node.js 24.22.0 和 npm 11.19.1，然后运行：

```sh
npm install --global android-publisher-cli
android-release app add demo
android-release publish demo
android-release status demo
```

首次配置无需 APK。账号凭据可通过终端或本地 Web 表单录入，验证后在本机安全保存，以后复用。APK 校验组件在首次发布时自动安装，无需配置 Android SDK 或 JDK。也可以提前运行 `android-release doctor --repair`。

日常发布也可直接指定文件，剩余渠道在交互中补齐：

```sh
android-release publish demo \
  --apk huawei=/absolute/path/one.apk \
  --apk honor=/absolute/path/two.apk \
  --notes-file /absolute/path/notes.txt
```

`--apk` 明确绑定渠道与文件。文件名不会决定渠道归属。提交前会展示应用、版本、各渠道 APK、账号和更新说明。只有用户明确确认后才上传和提审。

## 本地 Web 端

```sh
android-release web
```

命令启动 GitHub 风格的本地工作台，并打开浏览器。从应用开始，连续完成渠道接入、发布新版本和结果跟踪。Web 和 CLI 共用应用、账号及发布记录。

首次接入逐渠道填写商店应用和账号资料，验证密钥后显示接入情况。账号设置也支持更新和清除。JSON/PEM 支持本机私有文件路径或直接粘贴内容。验证成功后，CLI 和 Web 共用保存的凭据。环境页可安装或修复 APK 检查组件。

日常发布按“准备材料 → 检查并确认 → 提交与跟踪”完成。提交前可返回修改，页面保留本机草稿；密钥输入单独处理。耗时操作由本地服务执行，离开页面后可回来查看。关闭本地服务会停止后续处理，重新启动后根据已保存记录继续。

服务仅监听 `127.0.0.1`，按 Ctrl+C 停止。指定端口或手动打开浏览器时，使用 `web --port 3210 --no-open`。

## 查看与继续

```sh
android-release publish demo
android-release status demo
android-release history demo
```

再次进入发布流程，可以继续已有记录。已受理的渠道和已确认步骤会跳过。结果未决时，工具保留记录并阻止冲突写入，先引导核对。

提交受理、审核通过和公开上架分别展示。查询失败时保留上次结果和时间。无法自动确认的渠道，可以在后台核对后运行 `status demo --record-manual` 保存人工记录。

## 渠道验证范围

六个渠道目前以模拟接口测试为主，尚未完成逐个真实账号和公开上架验证。正式提交前，使用获准的测试应用验证账号权限与结果。

| 渠道 | 本次审核查询 | 本次上架确认 |
| --- | --- | --- |
| 华为 | 支持 | 支持 |
| 荣耀 | 支持 | 后台核对 |
| 小米 | 后台核对 | 后台核对 |
| OPPO | 支持 | 支持 |
| vivo | 支持 | 支持 |
| 应用宝 | 后台核对 | 后台核对 |

“支持”要求本机有本次受理记录，且远端证据能关联本次版本。六渠道均不能自动解除响应丢失的写入。人工核对记录单独展示，也不能解除缺乏关联证据的未决请求。

## 按任务查阅

| 任务 | 文档 |
| --- | --- |
| 安装与检查组件 | [安装说明](docs/getting-started.md) |
| 应用和渠道账号 | [应用配置](docs/configuration.md) |
| 每次发布的操作 | [发布流程](docs/release-workflow.md) |
| 结果查询与中断恢复 | [状态与恢复](docs/status-and-recovery.md) |
| 命令、参数和退出码 | [命令参考](docs/commands.md) |
| 用户故事和交互约定 | [设计说明](docs/cli-design.md) |
| Web 页面与用户故事 | [Web 交互约定](docs/web-experience.md) |
| 开发与测试 | [贡献指南](CONTRIBUTING.md)、[架构](docs/architecture.md) |
| 故障和密钥泄露 | [安全反馈](SECURITY.md) |

配置、APK 副本和发布记录位于本机私有目录。原创代码采用 [MIT 许可证](LICENSE)，运行组件见[第三方声明](THIRD_PARTY_NOTICES.md)。项目独立于各商店厂商。
