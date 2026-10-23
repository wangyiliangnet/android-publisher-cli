# 日常发布、查询与恢复

本页按六个渠道一起发布编写。开始前，请完成[首次配置](colleague-quickstart.md)，确认本地项目已配置全部渠道，并已导入凭据。

下面沿用项目名 `demo`，本次批次名为 `demo-1-0-0`。批次就是本次要提交的各渠道 APK、更新说明和发布设置。同一份内容准备后会固定保存；换包、改说明或改变发布意图时，使用新的批次名。

示例名称和路径均为合成内容。请替换为本机的项目名、批次名和绝对路径，并提前建好保存批准文件及报告的私有目录。

## 准备、预检与审阅

### 第 1 步：准备六个渠道的 APK 和更新说明

准备本次更新说明文本，以及华为、荣耀、小米、OPPO、vivo 和应用宝各自的 APK。每个已配置渠道都必须明确提供路径，工具不会搜索 APK 或选择最新文件。

```sh
release prepare --project demo --batch demo-1-0-0 \
  --notes "/absolute/path/release-notes.txt" \
  --apk huawei="/absolute/path/huawei.apk" \
  --apk honor="/absolute/path/honor.apk" \
  --apk xiaomi="/absolute/path/xiaomi.apk" \
  --apk oppo="/absolute/path/oppo.apk" \
  --apk vivo="/absolute/path/vivo.apk" \
  --apk tencent="/absolute/path/tencent.apk"
```

工具会检查包名、批准签名、版本名、版本号和文件哈希，再把 APK 与说明固定保存到本机。这个过程称为“冻结”。`prepare` 和 `plan` 都离线执行，不访问渠道。

检查输出的 `batch_id` 是否为本次批次名，`total_targets` 是否为 `6`，`entries` 中是否包含全部六个渠道。准备失败时，先按报错检查路径、包名、版本和签名；不要用其他 APK 反复覆盖同一批次。

### 第 2 步：检查各渠道账号和应用

```sh
release batch preflight --batch demo-1-0-0
```

预检会读取渠道状态，检查本次提交所需的账号和应用信息，并保存结果。它可能申请或刷新登录令牌，但不会上传 APK 或提审。

逐个查看六个渠道的结果，并检查命令退出状态。某个渠道失败时，按返回提示处理凭据、账号权限或应用配置，再重新预检。不要把一个渠道成功当成全部通过。

### 第 3 步：生成批准文件并核对

六个渠道预检通过后，生成本次批准文件：

```sh
release batch review --batch demo-1-0-0 \
  --file "/absolute/path/new-approval.json"
```

文件必须使用新的绝对路径，父目录须已存在。命令会显示提交预览，保存批准文件，并在输出中返回 `approval_plan`。这串摘要标识你这次审阅的准确内容，下一步提交时要原样使用。

核对预览和文件中的这些内容：

- 包名、公开账号和应用标识是否正确。
- 版本、六份 APK 及其签名哈希是否与本次发版一致。
- 更新说明是否正确；有说明变更对比时，一并核对。
- 渠道是否包含全部六个。
- 发布规则是否为 `after_approval`，即审核通过后立即发布。

批准文件同时绑定配置和已保存的预检。生成文件本身不会上传或提审，也不会覆盖旧批准文件。文件含发布详情，请保密保存。

如果发现 APK、配置、更新说明、预检或渠道选择有变化，先处理变化，再重新审阅。修改发布内容需要新批次；仅重新生成批准文件时，也应使用新的文件路径。

## 明确提交

### 第 4 步：提交整个批次

**下面的命令会真实上传六个渠道的 APK 并提审；审核通过后立即发布。** 确认已获得覆盖应用、产物和六个渠道发布范围的授权后再执行。

将 `APPROVAL_PLAN_FROM_REVIEW` 替换为上一步输出的 `approval_plan`，并使用刚才核对过的批准文件：

```sh
release batch submit --batch demo-1-0-0 \
  --approval "/absolute/path/new-approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

工具会在第一个渠道开始前检查全部选定输入。摘要、批准文件或当前固定保存的内容不匹配时，提交会被拒绝，需重新审阅。

执行后逐个检查渠道结果。“已受理”表示商店接收了提交，不代表审核通过或已经上架。部分渠道成功时，先保留记录，再按[安全恢复](#安全恢复)处理剩余渠道。

## 查询与报告

### 第 5 步：刷新进度

```sh
release batch status --batch demo-1-0-0 --refresh
```

这会查询各渠道，并显示整个批次的状态。去掉 `--refresh` 时只读取本地缓存。

先看各渠道是否已受理，再分别看审核和上架结果。查询失败时，检查来源健康状态和采集时间；保留的旧数据不会变成新结果。小米推测、应用宝版本关联等含义见[状态解释](channels/automatic-observation.md)。

需要持续查看时运行：

```sh
release batch watch --batch demo-1-0-0 --format table
```

`watch` 定期执行只读查询，并遵守各目标已保存的下次检查时间。按 Ctrl-C 停止查看，不会取消渠道审核。自动查询无法确认具体问题时，再按提示到渠道后台核实。

### 第 6 步：查历史并导出报告

查看某个包的本地发布历史：

```sh
release history --package com.example.app --limit 20
```

导出整个批次的 Markdown 报告：

```sh
release report --batch demo-1-0-0 \
  --file "/absolute/path/new-report.md"
```

若只需要单个渠道的记录，可以按发布 ID 导出 JSON。发布 ID 可从本地历史中查看，下面使用 OPPO 的示例：

```sh
release report --release demo-1-0-0-oppo \
  --format json --file "/absolute/path/new-report.json"
```

`history` 和 `report` 都只读本地缓存。需要较新的报告时，先刷新状态，再导出。报告文件必须使用新的绝对路径；父目录须已存在，路径中不能有符号链接。

报告包含所选发布或整个已保存批次的操作摘要、证据来源、时间和哈希。报告会排除凭据、凭据引用、私有账号配置、远端操作引用和原始响应，但应用信息、更新说明及驳回原因仍可能敏感，分享前请检查。

## 安全恢复

### 提交出错或终端中断后，先核对

网络错误或进程结束，不代表渠道没有收到请求。先查看本地记录，再运行只读核对：

```sh
release batch status --batch demo-1-0-0
```

```sh
release batch reconcile --batch demo-1-0-0
```

核对会尝试确认已发送操作的结果，不会重新上传或提审。查看返回的 `next_action` 和各渠道结果。若仍存在结果未知的写入，工具会阻止冲突目标继续写入。

### 核对允许继续后，再恢复提交

仍使用本次审阅过的批准文件和摘要：

```sh
release batch resume --batch demo-1-0-0 \
  --approval "/absolute/path/new-approval.json" \
  --approve-plan APPROVAL_PLAN_FROM_REVIEW
```

`resume` 会跳过已受理目标，并重新检查批准内容和未决写入。批准内容已变化时，按提示重新审阅；结果仍未知时，保留记录并调查具体操作。

不要编辑 SQLite、删除操作记录或新建发布来绕过阻塞，也不能用小米推测、应用宝未关联的信息作为核对依据。渠道特定的授权替换或续期是单渠道操作，需按该渠道提示单独处理，不会自动应用到整个批次。

## 本地文件与备份

| 内容 | 本机位置 |
| --- | --- |
| 运行状态 | `~/Library/Application Support/AndroidPublisherCLI/` |
| 模拟状态 | `~/Library/Application Support/AndroidPublisherCLI/Simulation/` |
| 日志 | `~/Library/Logs/AndroidPublisherCLI/` |
| 钥匙串服务前缀 | `io.github.wangyiliangnet.android-publisher-cli.v1` |

### 创建备份

准备一个尚不存在的绝对目录路径，然后运行：

```sh
release state backup --to "/absolute/path/new-backup-directory"
```

备份含敏感数据，请私下保存。它不是完整的整机迁移包；凭据、全部可复用配置和输入文件，以及外部账号权限，需要另行处理。

### 恢复备份后检查限制

只有确实需要恢复本地数据时才使用备份恢复。提交失败时，先按上面的“安全恢复”核对，不要直接还原旧备份。

```sh
release state restore --from "/absolute/path/backup-directory"
```

恢复后运行：

```sh
release state recovery-status
```

此时会启用恢复屏障，暂时阻止业务写入。必须核对完整账号和应用清单、远端历史及未决操作后，才能按恢复检查继续。仅恢复文件不会获得新的发布授权；核对不清时保留屏障，不要直接修改数据库。

## 输出约定

大多数命令向 stdout 输出结构化 JSON。配置向导的提示和批次审阅的预览写入 stderr，所以它们可能与 JSON 同时显示在终端里。状态和历史也支持供人工阅读的表格；编写脚本时，可在命令支持的情况下使用 `--format json`。

判断命令是否完成，要同时检查进程退出状态、`effect` 和 `next_action`。退出码 `130` 表示取消；如果取消发生在提交期间，仍需按恢复步骤核对已发送的操作。鉴权、配置、本地状态和未决操作的处理方式不同，应按返回提示操作。

### 其他输入方式与部分渠道操作

通常按上面的项目方式准备六个渠道。如果已有文件式输入流程，可以先创建模板：

```sh
release init --file "/absolute/path/new-input.json"
```

编辑新模板，只填写公开标识、钥匙串引用和明确的发布输入，再用这个文件准备批次：

```sh
release prepare --file "/absolute/path/new-input.json"
```

文件方式不能与 `--project`、`--batch`、`--notes` 或 `--apk` 混用。

确实只获准提交部分渠道时，在审阅、提交和继续执行时使用相同的 `--channels oppo,honor`，将示例值换成获准渠道。批准文件会绑定这个选择。结果仍展示完整批次，部分渠道成功不代表整个批次完成。
