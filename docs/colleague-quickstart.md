# 首次使用与本机配置

第一次使用时，按本页安装工具、配置应用并导入六个渠道的凭据。完成后，按[日常发布](daily-cli-workflow.md)提交版本。日常使用不需要先登录浏览器。

## 1. 安装并检查环境

### 准备安装材料

准备一台 Apple Silicon Mac，并安装 Android SDK Build Tools 和 JDK。检查 APK 时需要 Build Tools 和 JDK，通常可以使用 Android Studio 已安装的版本。运行 API 命令不需要安装 Go 或 Node.js。

向维护者索取对应版本的 `darwin-arm64` 安装包和 `.sha256` 校验文件，或从项目 GitHub Releases 下载。把两个文件放在同一目录。在终端进入该目录。

先确认安装包的用途。带 `ci-preview` 的包用于测试预览。它使用 ad-hoc 临时签名，未公证，也没有跨版本稳定的钥匙串信任身份。**导入真实凭据前，应取得团队固定签名包，并先验证测试应用及账号权限。** 解压后，阅读包内的 `DISTRIBUTION.txt`，核对安装包的实际状态。

### 校验、解压并安装

下面用 CI 预览包演示安装。版本号和文件名只是示例，请替换为你实际取得的文件名。固定签名包的文件名没有 `-ci-preview` 后缀。

先校验下载文件：

```sh
shasum -a 256 -c android-publisher-cli-0.1.0-beta.1-darwin-arm64-ci-preview.tar.gz.sha256
```

确认输出中对应文件为 `OK`。校验失败时停止安装，重新取得安装包和配套校验文件。

再解压并进入安装包目录：

```sh
tar -xzf android-publisher-cli-0.1.0-beta.1-darwin-arm64-ci-preview.tar.gz
cd android-publisher-cli-0.1.0-beta.1-darwin-arm64-ci-preview
```

阅读 `DISTRIBUTION.txt` 后运行：

```sh
/bin/sh install.sh
```

默认安装到 `~/.local/android-publisher-cli/`。需要其他位置时，使用 `--prefix "/absolute/dedicated/directory"`，选择当前用户所有的独立目录。

### 让终端找到命令

使用默认安装位置时，在当前终端运行：

```sh
export PATH="$HOME/.local/android-publisher-cli/bin:$PATH"
```

再检查版本：

```sh
release version --json
```

确认输出的版本与安装包一致。若提示找不到 `release`，先检查 PATH 是否指向安装位置。上面的 `export` 只对当前终端生效。要让新终端也能使用命令，请把同一行加入你所用 shell 的启动配置。

### 检查 APK 检查工具

```sh
release doctor
```

按检查结果处理缺失的 SDK 或 JDK。无法自动识别已安装的工具时，明确指定路径：

```sh
release --sdk-root "/absolute/path/android-sdk" \
  --java-home "/absolute/path/jdk" doctor
```

请将示例路径换成本机路径。后续检查 APK 时，若命令仍无法自动识别工具，在命令前使用相同的全局参数。

## 2. 配置项目并导入凭据

### 选一份可信 APK，创建项目

准备一份你已确认可信、已签名且不可调试的 APK。工具将用它的包名和签名匹配后续发布的 APK。这一步不会核实商店旧版签名。

```sh
release setup --project demo --apk "/absolute/path/trusted.apk"
```

`demo` 是本地项目名，后续每次发版都用它。把 APK 路径替换为本机的绝对路径。然后按向导操作：

1. 核对显示的包名和签名 SHA-256，确认后继续。
2. 在“选择渠道”处按回车，保留默认的全部六个渠道。
3. 逐个填写下表中的账号信息和钥匙串条目名称。条目名称是保存凭据时使用的名字，不是密钥内容。
4. 核对最后的项目摘要，确认保存。

| 渠道标识 | 向导中需要的账号和应用信息 | 稍后要导入的钥匙串条目 |
| --- | --- | --- |
| `huawei`（华为） | 本机账号别名；语言标识可留空，由预检匹配 | `service-account`：整份服务账号 JSON |
| `honor`（荣耀） | 本机账号别名、账号级 API 的 client_id；语言标识可留空 | `client-secret` |
| `xiaomi`（小米） | 本机账号别名、发布 API userName、图标绝对路径、公开 HTTPS 隐私政策 URL | `api-secret`、`api-public-key`（公钥证书） |
| `oppo`（OPPO） | 公开 client_id，同时作为本机账号标识 | `client-secret` |
| `vivo` | 本机账号别名、公开 access_key | `access-secret` |
| `tencent`（应用宝） | 本机账号别名、账号 uid、应用 AppID | `api-secret` |

向导只在本机保存配置。它不读取钥匙串或访问渠道，也不保存样例 APK、版本或更新说明。JSON 输出中的 `saved` 为 `true` 时，表示项目已保存。

填错时，可输入 `cancel` 或按 Ctrl-D 取消。取消不会保存配置。需要替换已保存的项目时，在同一条 `setup` 命令末尾加 `--replace`。重新核对后，确认保存。

### 将各渠道凭据存入钥匙串

先确认使用的是团队固定签名包。然后按上表逐项导入凭据。每次导入时，三个参数都要与向导中保存的配置一致：

- `--channel`：表中的渠道标识。
- `--account`：向导里填写的本机账号别名；OPPO 使用其公开 client_id。
- `--name`：向导里填写的钥匙串条目名称。

例如，荣耀账号使用默认别名 `honor-main`、条目名称 `client-secret` 时：

```sh
release credentials import --channel honor --account honor-main --name client-secret
```

按隐藏输入提示粘贴密钥。不要把密钥写到命令参数里，以免进入 shell 历史。输出中的 `credential_state` 为 `stored`，表示已保存到钥匙串。

华为的服务账号 JSON 和小米的公钥证书适合从文件原样导入。文件必须是仅所有者可读的普通文件。以华为为例：

```sh
chmod 600 "/absolute/path/service-account.json"
release credentials import --channel huawei --account huawei-main \
  --name service-account --file "/absolute/path/service-account.json"
```

导入小米公钥证书时，改用 `--channel xiaomi`、配置中的小米账号别名和 `--name api-public-key`，并将 `--file` 指向证书文件。

继续为其余渠道导入表中的条目，直到六个渠道全部完成。小米需要导入密钥和公钥证书两项。若你在向导里改过条目名称，导入时使用改后的名字。

已有条目需要更新时，在导入命令末尾加 `--replace`。macOS 请求钥匙串授权时，只在系统窗口中输入密码。能保存或读取凭据，不代表账号已有渠道发布权限；后续还要运行预检。

完成后，前往[日常发布](daily-cli-workflow.md)，准备本次更新说明和六个渠道的 APK。

## 3. 升级、回滚与卸载

### 升级到新版本

1. 停止正在运行的发布和查询命令。
2. 按本页安装步骤校验并解压新包，进入新包目录。
3. 运行新包中的 `/bin/sh install.sh`。若原来用了 `--prefix`，这次使用相同路径。
4. 运行 `release version --json`，确认已经切换到新版本。

安装程序会检查哈希、代码签名、架构、所有权和路径安全。安装后保留版本目录，并切换 `current` 链接。若相同版本号的文件内容不同，安装程序会拒绝安装。此时需要使用新版本号。校验和与签名检查通过，不代表已获得 Apple 信任或完成公证。

### 切回已安装的版本

先停止正在运行的命令。然后用受管理安装包中的安装程序切换版本。下面的版本号只是示例，目标版本必须已经安装：

```sh
/bin/sh install.sh --rollback 0.1.0-beta.1
```

自定义安装位置时，仍需加上原来的 `--prefix`。切换后运行 `release version --json` 核对版本。

回滚只切换本机程序，不会撤销商店写入、降级本地状态结构或跳过恢复检查。本地状态变化后，先确认旧版本是否兼容。

### 卸载命令

```sh
/bin/sh install.sh --uninstall
```

自定义安装位置时，同样加上原来的 `--prefix`。卸载只删除受管理的命令链接和标记，保留版本文件、发布数据和钥匙串条目。数据位置及备份方法见[本地文件与备份](daily-cli-workflow.md#本地文件与备份)。
