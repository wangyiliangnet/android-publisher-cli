# Issue tracker: GitHub

任务和规格记录在 `wangyiliangnet/android-publisher-cli` 的 GitHub Issues，使用 `gh` CLI 读写。

## 目标仓库

每次会话首次写入前，运行 `gh repo view --json nameWithOwner`，核对目标为 `wangyiliangnet/android-publisher-cli`。目标不符时先纠正仓库；目标归属不明时询问用户。

## 常用操作

多行正文先保存到临时文件，再使用 `--body-file`。

| 操作 | 命令 |
| --- | --- |
| 创建 | `gh issue create --title "<title>" --body-file "<file>"` |
| 读取 | `gh issue view <n> --json number,title,body,labels,comments` |
| 列出 | `gh issue list --state open --json number,title,body,labels,comments`，按需添加 `--label` |
| 更新正文 | `gh issue edit <n> --body-file "<file>"` |
| 评论 | `gh issue comment <n> --body-file "<file>"` |
| 添加或移除标签 | `gh issue edit <n> --add-label "<label>"` / `--remove-label "<label>"` |
| 认领 | `gh issue edit <n> --add-assignee @me` |
| 关闭 | 先评论说明结果，再运行 `gh issue close <n>` |

技能要求“发布到 issue tracker”时创建 GitHub issue；要求“获取相关 ticket”时读取对应 issue。

## 子任务与阻塞关系

优先使用 GitHub 原生子任务和依赖关系。通过 `gh api` 操作，路径中的 `{owner}/{repo}` 由当前仓库解析：

- 子任务：`POST repos/{owner}/{repo}/issues/<parent>/sub_issues`，传入 `-F sub_issue_id=<child-db-id>`。
- 阻塞关系：`POST repos/{owner}/{repo}/issues/<child>/dependencies/blocked_by`，传入 `-F issue_id=<blocker-db-id>`。
- 数据库 ID：`gh api repos/{owner}/{repo}/issues/<n> --jq .id`。关系接口使用数据库 ID。
- 未关闭的阻塞项数量：`gh api repos/{owner}/{repo}/issues/<n> --jq .issue_dependencies_summary.blocked_by`。

接口不可用时，在子任务正文顶部记录 `Part of #<parent>` 和 `Blocked by: #<n>, #<n>`，在父 issue 正文维护任务清单。所有阻塞项关闭后，任务才能开始。

## Pull requests as a triage surface

**PRs as a request surface: no.**

裸编号 `#42` 可能指 issue 或 PR。先用 `gh pr view 42` 判断，再按需用 `gh issue view 42` 读取。

## Wayfinding operations

`wayfinder` 使用一个带 `wayfinder:map` 标签的 issue 记录 Notes、Decisions-so-far 和 Fog。子任务关联到该 issue，按类型标记 `wayfinder:research`、`wayfinder:prototype`、`wayfinder:grilling` 或 `wayfinder:task`。

按 map 中的顺序选择首个未关闭、无阻塞且无人认领的子任务，开始前认领。完成后先评论结论，再关闭子任务，并把结论摘要及链接补入 map 的 Decisions-so-far。
