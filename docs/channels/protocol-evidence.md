# 渠道协议与验证依据

本页供修改渠道请求的维护者使用。新版 TypeScript 适配器只支持已有应用的版本更新，发布规则为 `after_approval`。`android-release protocols` 可在本机查看实现范围。

## 当前能力

| 渠道 | 写入步骤 | 仍需注意 |
| --- | --- | --- |
| 华为 | 申请上传授权、上传、绑定、更新中文说明、提审 | 上传地址和签名头必须匹配授权；绑定后至少等待 2 分钟再提审 |
| 荣耀 | 申请上传授权、上传、绑定、更新中文说明、提审 | 审核查询需要本次提交的 `releaseId`；更新语言时使用 `setAll: 0` |
| 小米 | 签名后推送 APK 与图标 | 包查询没有版本绑定的审核结果；测试账号按结构化 JSON 输入 |
| OPPO | 申请上传授权、上传、提审 | 授权本地使用期限为 30 秒；预检需要完整的已有应用元数据 |
| vivo | 上传、提审 | 单 APK 更新；签名方法及账号权限仍待真实账号核对；请求间隔至少 1 分钟 |
| 应用宝 | 申请上传授权、上传、提审 | 使用主开发者账号；审核接口未给出本次版本标识 |

六个适配器通过了合成响应测试。测试覆盖只读预检、写入步骤、回执保存和状态分离。**`protocol_mock_verified` 只表示合成测试通过。** `account_verified` 和 `live_verified` 尚无新版真实账号证据。不要从测试结果推断渠道授权、审核结果或公开上架。

响应丢失时，渠道查询通常缺少足够的操作标识或产物摘要。新版 `reconcile` 保留未决写入，不自动重试。补充恢复逻辑前，必须记录可核对的官方字段和对应测试。

## 官方资料

下表沿用 2026-10-23 整理的官方入口。页面可能调整、要求登录或动态加载。修改协议前，重新核对实际接口页面和账号响应。

| 渠道 | 官方入口 | 核对重点 |
| --- | --- | --- |
| 华为 | [AppGallery Connect API 简介](https://developer.huawei.com/consumer/en/doc/AppGallery-connect-Guides/agcapi-overview-0000001158245083) | 服务账号、上传授权、应用信息和提审字段 |
| 荣耀 | [API 包上传指南](https://developer.honor.com/cn/doc/guides/101359) | OAuth、应用标识、上传对象和发布结果 |
| 小米 | [自动发布 API 指南](https://dev.mi.com/xiaomihyperos/documentation/detail?pId=1134) | SIG、包查询、推送和测试账号结构 |
| OPPO | [开发者服务中心](https://open.oppomobile.com/) | 令牌、上传地址、已有应用元数据和提审结果 |
| vivo | [API 接入指南](https://developer.vivo.com/doc/detail?id=73) | 签名方法、账号平台和发布字段 |
| 应用宝 | [开发者 API 指南](https://wiki.open.qq.com/index.html#/guide/develop?id=4015262492) | 主开发者账号、文件序列号和审核查询 |

在 PR 中写明文档页面、字段、访问限制和仍待账号验证的行为。新增成功、错误及响应丢失的合成用例。不能把缺失字段、未知状态码或旧版本信息解释为本次发布成功。

协议代码独立实现。第三方来源和许可证见[第三方声明](../../THIRD_PARTY_NOTICES.md)。
