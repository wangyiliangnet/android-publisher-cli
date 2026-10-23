# Mac 同事使用手册

本工具在 Apple Silicon Mac 上准备和提交六渠道 Android 更新。程序名为 `release`。渠道账号、凭据和 APK 保留在本机；日常查询不要求先登录浏览器或手工录入审核状态。

当前为公开测试版。CI 包采用临时的 ad-hoc 签名，未公证，也不提供跨版本稳定的钥匙串信任身份，适合先做模拟验收。使用真实凭据前，应采用团队固定签名的安装包，并先核验测试应用和账号权限。详见[安装与分发说明](mac-cli-delivery.md)。

## 1. 安装和检查

从项目的 GitHub Releases 下载对应版本的 `darwin-arm64` 压缩包及 `.sha256` 文件，放在同一目录。先查看该 Release 的分发说明，再校验和安装。例如 CI 预览包：

```sh
shasum -a 256 -c android-publisher-cli-0.1.0-beta.1-darwin-arm64-ci-preview.tar.gz.sha256
tar -xzf android-publisher-cli-0.1.0-beta.1-darwin-arm64-ci-preview.tar.gz
cd android-publisher-cli-0.1.0-beta.1-darwin-arm64-ci-preview
/bin/sh install.sh
```

默认安装在 `~/.local/android-publisher-cli/`。把其 `bin` 目录加入 PATH，或直接使用完整路径：

```sh
export PATH="$HOME/.local/android-publisher-cli/bin:$PATH"
release version --json
release doctor
```

执行 APK 检查需要 Android SDK Build Tools 和 JDK。通常可以使用 Android Studio 已安装的 SDK 和 JDK。无法自动识别时，在命令前指定：

```sh
release --sdk-root "/absolute/path/android-sdk" \
  --java-home "/absolute/path/jdk" doctor
```

## 2. 一次性配置项目

先准备一份明确可信、已签名且不可调试的 APK。向导会显示包名和签名 SHA-256，请核对后确认。这是今后本地包的匹配基准，不代表已核实商店旧版签名。

```sh
release setup --project demo --apk "/absolute/path/trusted.apk"
```

按提示选择渠道，填写公开标识和钥匙串条目名称。需要的公开信息如下：

| 渠道 | 公开配置 | 凭据条目名称示例 |
| --- | --- | --- |
| 华为 | 本机账号别名；可选语言标识 | `service-account`：整份服务账号 JSON |
| 荣耀 | 本机账号别名、账号级 API 的 client_id | `client-secret` |
| 小米 | 本机账号别名、发布 API userName、图标、隐私政策 URL | `api-secret`、`api-public-key` |
| OPPO | 公开 client_id，同时作为本机账号标识 | `client-secret` |
| vivo | 本机账号别名、公开 access_key | `access-secret` |
| 应用宝 | 本机账号别名、账号 uid、应用 AppID | `api-secret` |

不要在向导中输入密钥。项目只保存公开配置和凭据引用，不保存样例 APK、版本或更新说明。输入 `cancel` 或按 Ctrl-D 可取消；替换已有项目需要 `--replace` 和最终确认。

使用固定签名包时，另行导入凭据。以下均为占位示例，账号别名必须与向导一致：

```sh
release credentials import --channel honor --account honor-main --name client-secret
```

按隐藏输入提示粘贴密钥；不要把密钥放在命令参数里。华为整份 JSON、小米公钥证书可通过文件导入。文件应仅当前用户可读：

```sh
chmod 600 "/absolute/path/service-account.json"
release credentials import --channel huawei --account huawei-main \
  --name service-account --file "/absolute/path/service-account.json"
```

macOS 可能询问是否允许程序读取钥匙串。密码只在系统窗口输入。凭据成功存入或可读取，并不代表渠道已经授予发布权限。

## 3. 准备本次更新

为项目所选的每个渠道明确提供 APK，所有包必须匹配项目的包名、批准签名和同一版本。下例假设 `demo` 只配置了 OPPO、荣耀两个渠道：

```sh
release prepare --project demo --batch demo-1-0-0 \
  --notes "/absolute/path/release-notes.txt" \
  --apk oppo="/absolute/path/oppo.apk" \
  --apk honor="/absolute/path/honor.apk"
```

此步骤离线检查并冻结 APK、更新说明和批次，不上传或提审。若内容变化，请使用新的批次 ID。当前生产提交采用“审核通过后立即发布”。

## 4. 预检、审阅和提交

```sh
release batch preflight --batch demo-1-0-0
mkdir -p "$HOME/PublisherWork"
release batch review --batch demo-1-0-0 \
  --file "$HOME/PublisherWork/demo-approval.json"
```

预检查询账号和应用现状。审阅会展示账号、应用、版本、APK 摘要、更新说明、发布规则，并生成不可变批准文件。检查该文件，确认后把返回的 `approval_plan` 完整填入：

```sh
release batch submit --batch demo-1-0-0 \
  --approval "$HOME/PublisherWork/demo-approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

这条命令会真实上传并提交审核。批准摘要必须来自本次审阅，不要使用他人的摘要或自动跳过审阅。

## 5. 一条命令查询，按需导出

```sh
release batch status --batch demo-1-0-0 --refresh
release history --package com.example.app
release report --batch demo-1-0-0 \
  --file "$HOME/PublisherWork/demo-report.md"
```

状态中“已受理”“审核通过”“已上架”是三种不同结果。小米审核仅为版本比较推测；应用宝会显示审核信息、脱敏驳回原因和公开商店版本，未关联到本次版本时明确注明。部分查询失败时，查看每个来源的健康状态、时间和缓存标记。详见[自动查询说明](channels/automatic-observation.md)。

报告是本地缓存导出，不会刷新渠道。虽然不包含密钥和原始响应，仍可能包含应用信息和更新说明，分享前请检查。

## 6. 中断或异常

不要因超时而重新新建发布或反复提交。先查看状态，再执行只读核对：

```sh
release batch reconcile --batch demo-1-0-0
release batch resume --batch demo-1-0-0 \
  --approval "$HOME/PublisherWork/demo-approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

已受理渠道会跳过；结果不明的写入会继续阻塞。只有遇到无法自动确认的具体问题时，才根据提示到渠道后台核实，不要绕过本地记录。更多恢复、升级和备份说明见[日常工作流](daily-cli-workflow.md)。
