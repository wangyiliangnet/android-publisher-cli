# 开发者架构说明

本页面向修改代码的维护者，说明发布状态怎样保存，以及怎样避免中断后重复提交。使用步骤见[发布应用更新](release-workflow.md)，输出字段见[查询结果与处理中断](status-and-recovery.md)。

CLI 用 Node.js 内置 SQLite 保存发布批次、操作记录和渠道查询结果。应用配置是长期复用的输入；批次是某一次发布的不可变快照；目标（target）是批次中向一个渠道提交的更新。

## 代码分工

| 代码 | 职责 |
| --- | --- |
| `src/cli.ts` | 解析命令，组织交互和结果输出 |
| `src/doctor.ts` | 检查本机 SDK、Java 和工具启动，提供修复方法 |
| `src/setup.ts` | 收集应用配置，解释渠道字段，显示保存摘要 |
| `src/model.ts`、`src/private-file.ts` | 校验数据，计算校验值，安全读写本机文件 |
| `src/artifact.ts` | 调用 Android 工具检查 APK，保存并复核文件副本 |
| `src/engine.ts` | 准备批次、预检、生成提交计划、执行和查询 |
| `src/state.ts` | 保存 SQLite 操作记录、查询结果、令牌和上传授权 |
| `src/channels/` | 六个渠道各自的 HTTP 接口实现 |
| `src/channels/capabilities.ts` | 渠道能力说明、更新说明上限和 APK 架构校验 |
| `src/credentials.ts` | 隐藏输入密钥，读取私有 JSON、PEM 文件 |

## 从配置到提交

`setup` 保存商店应用编号、API 账号标识等资料。`account` 是本机账号备注，OPPO 使用 `client_id`。备注参与令牌管理和冲突写入检查，同一个真实账号须保持稳定。重配时沿用旧备注和显式 `language_id`；新配置的中文条目由预检自动查找。

`prepare` 在复制产物前检查渠道更新说明上限、小米图标可读性、应用宝 APK 架构，以及所有 APK 的签名、包名和版本是否一致。确认后复制 APK 与小米图标，再保存不可变批次。确认后再次检查输入，源 APK 发生变化时保存失败。这一步不访问渠道。

`batch preflight` 读取商店账号和应用资料，结果绑定到批次。荣耀、OPPO 的令牌申请与刷新属于账号访问操作，单独保存状态。`batch review` 根据批次、所选渠道、APK 和预检校验值生成提交计划（代码中的 `Approval`）。这一步离线运行。

`batch submit` 在第一个业务请求前复核提交计划和全部选定文件。每个业务步骤先向 `operations` 表提交 `intent`，再发送一次请求。取得可验证回执后，立即保存 `confirmed_applied`。上传授权中的签名 URL 只保存在本机私有材料表中，不进入报告。

进程中断或响应无法验证时，记录保留 `intent` 或 `unknown`，表示请求是否执行仍不确定。同一渠道、账号和应用的未确定请求会阻止其他批次的冲突写入。

## 中断恢复和状态查询

`resume` 跳过已确认步骤，不重发结果未确定的步骤。`reconcile` 只读取能够核对原请求的远端证据。当前六个渠道尚无足够证据在响应丢失后自动解除阻塞，需要维护者核查；补充恢复实现前须先补充协议依据和测试。

令牌申请区分 `confirmed_rejected`、`confirmed_not_applied` 和 `unknown`。前两种允许修正后重试，未决申请继续阻塞。`summary` 的 `recovery` 列出未决令牌和同一写入范围内的操作，包含原批次、步骤、操作编号及下一步。该诊断不读取或公开令牌和上传授权。`reconcile` 遇到未决令牌时直接报告阻塞。

执行、审核、上架和查询是否成功分别保存。渠道受理只确认执行。查询结果须能关联本次版本，且本地已有本次受理记录，才更新审核与上架状态。其他信息写入 `channel_context`，不会增加已确认通过或上架的数量。

## 本机文件与诊断边界

状态位于 `~/Library/Application Support/AndroidPublisherCLI/v2/`。SQLite 使用 WAL 和完全同步设置。文件与目录限制为当前用户访问。

终端输入的密钥只留在当前进程。华为服务账号 JSON、小米公钥或证书及可选测试账号 JSON 从私有文件读取；文件须属于当前用户，权限为 `0600`。短期令牌和上传授权保存到私有状态库。

`doctor` 不访问渠道或创建发布状态。它检查路径、文件访问权限，启动 Java 与 Android 工具，再生成可读诊断或 JSON。JSON 保留原有路径字段，并增加 `checks`；`ready` 表示所有检查通过。`AndroidTools.diagnostic()` 仍只返回路径发现结果，供内部测试判断工具是否已安装。

## 测试覆盖

`test/recovery.test.mjs` 在 HTTP 请求返回后终止子进程，检查重启后是否阻止重复写入。`test/android.test.mjs` 构建并验签临时 APK。`test/channels.test.mjs` 用模拟响应覆盖六个渠道。`test/doctor.test.mjs` 和 `test/setup.test.mjs` 验证诊断修复提示、工具启动与配置兼容性。

`test/auth.test.mjs` 检查令牌拒绝后的重试、响应丢失和重启后的阻塞，以及诊断不暴露私有材料。`test/workflow.test.mjs` 检查冻结前的本机校验和确认后的再次检查。`test/capabilities.test.mjs` 检查能力披露与离线命令。

这些测试不证明真实账号权限或商店未公开行为，真实验证范围见[渠道接口与验证范围](channels/protocol-evidence.md)。
