# 日常发布、查询与恢复

面向已完成[首次配置](colleague-quickstart.md)的发版同事。每份不同的发布意图使用新的不可变批次 ID；以下示例均为合成标识和绝对输入路径。当前生产提交采用 `after_approval`，即审核通过后立即发布。

将示例路径替换为本机路径，并提前准备保存批准文件和报告的私有工作目录。

## 准备、预检与审阅

```sh
release prepare --project demo --batch demo-1-0-0 \
  --notes "/absolute/path/release-notes.txt" \
  --apk oppo="/absolute/path/oppo.apk" \
  --apk honor="/absolute/path/honor.apk"
release batch preflight --batch demo-1-0-0
release batch review --batch demo-1-0-0 \
  --file "/absolute/path/new-approval.json"
```

本例假设项目只配置 OPPO 和荣耀。每个已配置渠道都必须明确提供 APK。准备阶段会检查包名、批准签名、版本名、版本号和文件哈希，再冻结输入；不会替你搜索 APK 或选择最新文件。

`prepare` 和 `plan` 离线执行。`preflight` 读取渠道状态，可能协调令牌生命周期，但不能上传或提审。`batch review` 在本机检查，要求选定渠道已有保存的预检，并创建新的批准文件，不覆盖旧文件。

请审阅包名、公开账号和应用标识、版本、APK 与签名哈希、更新说明、可用时的说明变更、选定渠道，以及 `after_approval` 发布规则。返回的 `approval_plan` 覆盖这些输入及其冻结绑定。批准文件包含发布详情，应保密保存。

也可使用文件方式：先运行 `release init --file "/absolute/path/new-input.json"`，再运行 `release prepare --file "/absolute/path/input.json"`。只用公开标识和钥匙串引用替换占位内容，不要将 `--file` 与项目准备参数混用。

## 明确提交

```sh
release batch submit --batch demo-1-0-0 \
  --approval "/absolute/path/new-approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

这条命令会真实上传并提审。摘要必须与已审阅文件及当前冻结输入完全匹配。首个渠道派发前，会先检查全部选定输入。APK、配置、更新说明、预检或渠道选择发生变化时，需按变更情况重新审阅。

仅提交部分渠道时，在审阅和提交或继续执行时指定相同的 `--channels oppo,honor`。批准文件会绑定该选择。结果仍包含完整批次范围，部分渠道成功不等于整个批次成功。

## 查询与报告

```sh
release batch status --batch demo-1-0-0 --refresh
release batch watch --batch demo-1-0-0 --format table
release history --package com.example.app --limit 20
release report --batch demo-1-0-0 \
  --file "/absolute/path/new-report.md"
release report --release demo-1-0-0-oppo \
  --format json --file "/absolute/path/new-report.json"
```

不带 `--refresh` 时，批次状态读取本地缓存。`watch` 定期执行只读查询，并遵守各目标已持久化的下次检查时间。Ctrl-C 停止观测，不会取消渠道审核。

状态的含义、小米审核推测、应用宝版本关联，以及部分失败和缓存的判断，统一见[状态解释](channels/automatic-observation.md)。遇到自动查询无法确认的具体问题时，再按提示到渠道后台核实。

`history` 和 `report` 只读本地缓存。报告覆盖完整的选定发布或已保存批次，包括操作摘要、证据来源、时间和哈希，不包含凭据、凭据引用、私有账号配置、远端操作引用和原始响应。应用信息、更新说明和驳回原因仍可能敏感，分享前请检查。报告目标必须是新建的绝对路径文件，其父目录已存在且路径中没有符号链接。

## 安全恢复

```sh
release batch reconcile --batch demo-1-0-0
release batch resume --batch demo-1-0-0 \
  --approval "/absolute/path/new-approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

核对只读。继续执行会跳过已受理目标，并保留准确的批准检查。未知写入会阻塞冲突目标，不会再次发送。渠道特定的授权替换或续期属于特殊的单渠道操作，不会静默应用到整个批次。

网络错误或进程被终止不能证明远端没有生效。CLI 无法通过已验证证据确认结果时，应保留记录并调查具体操作。不要编辑 SQLite、删除操作、新建发布绕过阻塞，或用小米和应用宝的参考信息作为核对依据。

## 本地文件与备份

- 运行状态：`~/Library/Application Support/AndroidPublisherCLI/`
- 模拟状态：`~/Library/Application Support/AndroidPublisherCLI/Simulation/`
- 日志：`~/Library/Logs/AndroidPublisherCLI/`
- 钥匙串服务前缀：`io.github.wangyiliangnet.android-publisher-cli.v1`

```sh
release state backup --to "/absolute/path/new-backup-directory"
release state recovery-status
```

备份包含敏感数据，不等于整机迁移。凭据、全部可复用配置和输入文件、外部账号权限需另行处理。使用 `release state restore --from "/absolute/path/backup-directory"` 恢复后会启用恢复屏障，写入前必须完成清单核对。恢复备份不是处理提交失败的常规方法。

## 输出约定

大多数命令向 stdout 输出结构化 JSON；配置向导和批次审阅将提示或预览写入 stderr。状态和历史表格供人工阅读，脚本可在支持时使用 `--format json`。判断结果时应检查进程退出状态及 `effect`/`next_action`，不能只看某一行是否像成功提示。退出码 `130` 表示取消。鉴权、配置、本地状态和未决操作故障的处理方式不同，应遵循返回的安全操作提示，不要对所有错误一律重试。
