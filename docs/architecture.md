# 架构说明

CLI 使用 Node.js 内置 SQLite 保存批次、操作和观测。修改执行或恢复逻辑前，先核对本页的持久化顺序。操作命令见[发布应用更新](release-workflow.md)。

| 代码 | 职责 |
| --- | --- |
| `src/cli.ts` | 解析命令，调用批次操作，输出 JSON |
| `src/model.ts`、`src/private-file.ts` | 校验输入、计算摘要、读写私有文件 |
| `src/artifact.ts` | 用 Android 工具验签 APK，冻结并复核产物 |
| `src/engine.ts` | 准备批次、预检、审阅、执行和查询 |
| `src/state.ts` | 保存 SQLite 意图、回执、观测、令牌和授权材料 |
| `src/channels/` | 六个渠道的独立 HTTP 协议实现 |
| `src/credentials.ts` | 终端隐藏输入与私有 JSON、PEM 材料读取 |

## 批次与提交

`setup` 保存渠道公开账号和应用标识。`prepare` 验签并核对所有 APK 的包名、版本和签名，向操作者显示包名与版本。

确认后，`prepare` 将 APK 与小米图标复制到私有目录，再保存不可变批次。确认后若源 APK 变化，批次保存会失败。这两步不访问渠道。

`batch preflight` 只读查询远端账号和应用。荣耀、OPPO 的令牌申请属于账号生命周期操作，状态单独保存。`batch review` 将批次、所选渠道、APK 和预检的摘要写入批准文件。`batch submit` 在第一个业务请求前复核批准文件和全部选定产物。

每个业务步骤先向 `operations` 表提交 `intent`，再发送一次请求。取得可验证回执后，立即保存 `confirmed_applied`。上传授权的签名 URL 只保存在本机私有 SQLite 材料表，不进入报告。进程中断或响应无法验证时，日志保留 `intent` 或 `unknown`。

## 恢复与观测

`resume` 跳过已确认步骤，不重发未决步骤。`reconcile` 只读查询可关联的远端证据。当前六个渠道的响应丢失路径尚无足够依据自动解除阻塞；需要维护者核查。同一渠道、账号和应用的未决写入也会阻止新批次的冲突写入。

执行、审核、上架和查询健康分别保存。渠道受理提交只确认执行。观测与本次版本无法关联时，信息进入 `channel_context`，不会增加已核实的审核或上架数量。字段的操作含义见[状态查询与恢复](status-and-recovery.md)。

## 私有数据

状态位于 `~/Library/Application Support/AndroidPublisherCLI/v2/`。SQLite 使用 WAL 和完全同步设置。文件与目录限制为当前用户访问。

终端输入的渠道密钥只留在当前进程。华为 JSON、小米公钥证书及可选测试账号 JSON 从本机私有文件读取；文件须属于当前用户，权限为 `0600`。令牌和短期上传授权进入私有状态库，以便同一账号按已记录结果继续使用。

## 测试边界

`test/recovery.test.mjs` 在 HTTP 请求返回后终止子进程，再检查重启后的意图和重复写入阻塞。`test/android.test.mjs` 构建并验签临时 APK。`test/channels.test.mjs` 用合成响应覆盖六个渠道。这些测试不证明真实账号权限或商店未公开行为。
