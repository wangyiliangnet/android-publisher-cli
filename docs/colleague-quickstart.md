# 首次使用与本机配置

面向第一次使用 `release` 的发版同事。本页负责安装和一次性配置；项目配置好后，每次更新从[日常发布](daily-cli-workflow.md)开始。

## 1. 安装并检查环境

需要 Apple Silicon Mac。API 命令无需安装 Go 或 Node.js，APK 检查需要 Android SDK Build Tools 和 JDK，通常可使用 Android Studio 已安装的版本。日常查询无需先登录浏览器。

从维护者或项目 GitHub Releases 取得对应版本的 `darwin-arm64` 压缩包及 `.sha256` 文件，放在同一目录。先阅读随包 `DISTRIBUTION.txt`：CI 预览采用 ad-hoc 临时签名，未公证，没有跨版本稳定的钥匙串信任身份。使用真实凭据前，应取得团队固定签名包，并先核验测试应用和账号权限。

以下版本号仅作示例，安装时替换为实际取得的版本：

```sh
shasum -a 256 -c android-publisher-cli-0.1.0-beta.1-darwin-arm64-ci-preview.tar.gz.sha256
tar -xzf android-publisher-cli-0.1.0-beta.1-darwin-arm64-ci-preview.tar.gz
cd android-publisher-cli-0.1.0-beta.1-darwin-arm64-ci-preview
/bin/sh install.sh
```

默认安装在 `~/.local/android-publisher-cli/`，可用 `--prefix "/absolute/dedicated/directory"` 选择当前用户所有的独立目录。将对应 `bin` 目录加入 PATH；使用默认位置时：

```sh
export PATH="$HOME/.local/android-publisher-cli/bin:$PATH"
release version --json
release doctor
```

SDK 或 JDK 无法自动识别时，明确指定路径：

```sh
release --sdk-root "/absolute/path/android-sdk" \
  --java-home "/absolute/path/jdk" doctor
```

## 2. 配置项目并导入凭据

准备一份明确可信、已签名且不可调试的 APK。向导显示包名和签名 SHA-256，核对后确认，作为后续本地包的匹配基准；这不代表已核实商店旧版签名。

```sh
release setup --project demo --apk "/absolute/path/trusted.apk"
```

一个项目固定包名、批准签名和渠道集合。按提示选择渠道，填写公开标识和钥匙串条目名称：

| 渠道 | 公开配置 | 凭据条目名称示例 |
| --- | --- | --- |
| 华为 | 本机账号别名；可选语言标识 | `service-account`：整份服务账号 JSON |
| 荣耀 | 本机账号别名、账号级 API 的 client_id | `client-secret` |
| 小米 | 本机账号别名、发布 API userName、图标、隐私政策 URL | `api-secret`、`api-public-key` |
| OPPO | 公开 client_id，同时作为本机账号标识 | `client-secret` |
| vivo | 本机账号别名、公开 access_key | `access-secret` |
| 应用宝 | 本机账号别名、账号 uid、应用 AppID | `api-secret` |

向导不读取凭据、不访问渠道，只保存公开配置和凭据引用，不保存样例 APK、版本或更新说明。不要在向导中输入密钥。输入 `cancel` 或按 Ctrl-D 可取消，取消不保存；替换已有项目需 `--replace` 和最终确认。

使用固定签名包后，另行导入凭据。以下是占位示例，账号别名需与项目配置一致：

```sh
release credentials import --channel honor --account honor-main --name client-secret
```

按隐藏输入提示粘贴密钥，避免进入命令参数和 shell 历史。华为整份 JSON、小米公钥证书可用 `--file` 从仅所有者可读的普通文件原样导入：

```sh
chmod 600 "/absolute/path/service-account.json"
release credentials import --channel huawei --account huawei-main \
  --name service-account --file "/absolute/path/service-account.json"
```

替换已有条目需明确使用 `--replace`。macOS 可能请求钥匙串授权，密码只在系统窗口输入。能存入或读取凭据，不代表已获得渠道发布权限。

配置完成后，前往[日常发布](daily-cli-workflow.md)准备本次 APK 和更新说明。

## 3. 升级、回滚与卸载

切换程序前，先停止正在运行的发布和查询命令。校验并解压新包，用新包中的安装程序指定与原来相同的前缀；默认路径下仍运行 `/bin/sh install.sh`。

安装程序检查哈希、代码签名、架构、所有权和路径安全，保留版本目录并切换 `current` 链接。同版本内容不同会被拒绝，需使用新版本号。校验和与签名检查不等于 Apple 信任或公证。

切回已安装版本或卸载时，使用对应受管理安装包中的安装程序；以下版本号同样仅作示例：

```sh
/bin/sh install.sh --rollback 0.1.0-beta.1
/bin/sh install.sh --uninstall
```

回滚仅切换程序，不撤销商店写入、不降级本地状态结构，也不绕过恢复或版本检查。状态变化后先确认兼容性。卸载删除受管理的命令链接和标记，保留版本文件、发布数据及钥匙串条目。备份与恢复见[本地文件与备份](daily-cli-workflow.md#本地文件与备份)。
