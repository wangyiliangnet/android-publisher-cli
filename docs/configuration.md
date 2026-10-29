# 配置应用与渠道账号

应用配置告诉 CLI：这个应用要发布到哪些商店，各商店用哪个账号、对应哪个应用。配置保存后，每次发版只需提供新的 APK 和更新说明。

先在各商店后台创建好应用，并取得更新应用的 API 权限。CLI 只提交已有应用的版本更新。

## 创建应用配置

```sh
android-release setup --project demo
```

`demo` 是你给这份应用配置取的名字，例如 `reader`。它只供 CLI 查找配置，不是商店名称、App ID 或包名。名称使用字母、数字、点、横线和下划线，以字母或数字开头，最多 128 个字符。

向导先让你选择需要发布的渠道，首次配置不会默认全选。只选华为时输入 `huawei`；选择多个时输入 `huawei,honor`。可用名称如下：

| 商店 | 命令和配置中的名称 |
| --- | --- |
| 华为 | `huawei` |
| 荣耀 | `honor` |
| 小米 | `xiaomi` |
| OPPO | `oppo` |
| vivo | `vivo` |
| 应用宝 | `tencent` |

选择小米、OPPO 或 vivo 时，向导只询问一次应用包名，供这几个渠道共用。包名是 APK 内的应用标识，例如 `com.example.app`，须与商店中已有应用一致。可以先查看 APK：

```sh
android-release inspect --apk "/absolute/path/app.apk"
```

输出中的 `package_name` 是包名，`version_name` 是给用户看的版本名，`version_code` 是商店比较版本先后的整数。

## 按渠道准备资料

只需准备你选中的渠道。App ID 是商店给应用分配的编号，各商店可能不同；API 账号标识用于确定发起请求的账号或客户端。密钥用于证明你有权使用该账号，在连接渠道时再输入。

“账号备注”由你给本机保存的账号取名。每个渠道只有一个账号时，直接回车使用默认值，如 `huawei-main`。同一个渠道账号请始终使用同一备注；CLI 用它归集访问令牌和未完成操作，随意改名会拆散这些记录。OPPO 直接使用 `client_id`，不另取备注。

### 华为

在 [AppGallery Connect](https://developer.huawei.com/consumer/en/doc/AppGallery-connect-Guides/agcapi-overview-0000001158245083) 准备应用资料和服务账号。

- 向导中的 **App ID**：从该应用后台复制的应用编号。
- 连接时的 **服务账号 JSON 文件**：包含服务账号及私钥的文件。当前实现需要 `key_id`、`sub_account` 和 `private_key`。按终端提示输入文件绝对路径，CLI 用它为请求签名。

更新说明会写入后台已有的简体中文条目，预检时自动查找，无需填写“中文语言 ID”。若提示中文条目缺失或不唯一，先在后台核对语言配置，再重新预检。

### 荣耀

在[荣耀开发者平台](https://developer.honor.com/cn/doc/guides/101359) 准备应用编号和 API 客户端资料。

- 向导中的 **App ID**：该应用的商店编号。
- 向导中的 **client_id**：API 客户端 ID。
- 连接时的 **client_secret**：与该 `client_id` 配对的密钥。

更新说明同样自动选择后台已有的简体中文条目。`App ID` 标识应用，`client_id` 标识 API 客户端，两者分别填写。

### 小米

按[小米自动发布 API 指南](https://dev.mi.com/xiaomihyperos/documentation/detail?pId=1134) 准备发布资料。

- 向导中的 **userName**：自动发布 API 使用的开发者账号。
- **图标文件路径**：应用图标在本机的绝对路径。小米 API 提交更新时同时需要图标。
- **隐私政策地址**：应用隐私政策的 HTTPS 网页地址，不带查询参数或 `#` 片段。
- 连接时的 **api_secret**：API 密钥。
- 连接时的 **public_key_pem 文件**：API 使用的 RSA 公钥或证书，以 PEM 文本保存，供 CLI 生成请求签名材料。
- 可选的 **test_account_json 文件**：供审核人员登录应用的测试账号资料；不需要提供时直接回车。文件结构按官方 API 的测试账号字段准备。

应用标识直接使用前面填写的包名，无需再找一份商店 App ID。

### OPPO

在 [OPPO 开发者后台](https://open.oppomobile.com/) 准备 API 接入资料。

向导填写 **client_id**，连接时输入与它配对的 **client_secret**。应用标识使用包名。CLI 会读取商店中已有的应用资料，再提交版本更新。

### vivo

按 [vivo API 接入指南](https://developer.vivo.com/doc/detail?id=73) 取得账号的 API 接入资料。

向导填写 **access_key**，连接时输入与它配对的 **access_secret**。应用标识使用包名。当前实现每个批次向 vivo 提交一个 APK。

### 应用宝

按[应用宝开发者 API 指南](https://wiki.open.qq.com/index.html#/guide/develop?id=4015262492) 准备主开发者账号资料。

- 向导中的 **App ID**：该应用的 AppID。
- 向导中的 **uid**：主开发者账号的用户 ID。
- 连接时的 **api_secret**：该账号的 API 密钥。

AppID 和 uid 分别标识应用与账号，不能互换。

## 保存和修改

向导最后会逐渠道列出应用编号或包名、账号备注和 API 账号标识。核对后输入 `yes` 保存；随时输入 `cancel` 取消。成功时输出 `saved: true`。保存配置不会验证账号权限，首次连接检查在[发布前检查](release-workflow.md#检查渠道账号和现有版本)完成。

修改已有配置时运行：

```sh
android-release setup --project demo --replace
```

向导会用已有值作为默认值，包括账号备注，并保留旧配置中手动指定的更新说明语言。更改只影响以后创建的发布批次，已保存批次继续使用创建时的配置。若要改用另一个真实账号，也应更换账号备注。

## 账号密钥

运行预检、提交、继续执行、刷新状态或中断核对时，终端会提示输入所选渠道的密钥。短文本直接输入，输入过程不回显；JSON 和 PEM 材料输入文件路径。密钥不能写入应用配置或命令参数。

华为服务账号 JSON、小米公钥或证书、可选测试账号 JSON 须属于当前用户，且只有自己可读写。在提示输入路径前设置权限：

```sh
chmod 600 "/absolute/path/service-account.json"
chmod 600 "/absolute/path/xiaomi-public-key.pem"
```

按所用渠道选择相应文件。使用真实绝对路径，文件及其父目录不能经过符号链接。JSON 中的路径也必须写绝对路径，`~` 不会自动展开。

CLI 在一次命令运行中复用已输入的密钥。下次运行需要重新输入；荣耀和 OPPO 的短期令牌会记录在本机，用于后续请求。密钥文件保存在仓库外，不要放进 issue、日志或发布附件。

配置保存后，继续[发布应用更新](release-workflow.md)。
