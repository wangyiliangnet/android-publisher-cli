# 发布 CLI 版本

本页面向维护者，用于把 `android-publisher-cli` 的新版本发布到 npm，让用户安装或升级。流程是：验收安装包，创建版本 PR，合并后发布 GitHub Release，再确认 npm 已开放安装。

这里发布的是命令行工具本身。向 Android 商店提交应用更新，请使用[应用发布流程](release-workflow.md)和相应授权。

## 创建并合并版本 PR

先按 [npm 安装包验收](npm-package.md)检查交付物。在 GitHub Actions 中打开 [Prepare npm release](../.github/workflows/prepare-npm-release.yml)，选择 `main` 并运行工作流。

输入高于当前版本的 SemVer 版本号，例如 `0.4.0` 或 `0.4.0-beta.1`，不带 `v` 或构建元数据。工作流更新 `package.json` 与 `package-lock.json`，然后创建版本 PR。

核对 PR 中的版本号和包文件清单。由 `GITHUB_TOKEN` 创建的 PR 可能需要维护者在 GitHub 批准 CI 运行。等待 PR 的必需 `check` 通过，再按仓库规则 squash 合并。

## 发布 GitHub Release

确认合并后 `main` 顶端提交的必需 `check` 通过。在 GitHub Releases 建立 `vVERSION` 标签，目标选该提交，将 `VERSION` 换成实际版本号。

发布说明写用户可见变化、Node.js 要求、旧发布记录能否继续使用、已知限制和真实渠道账号验证情况。预发布版本勾选 **Set as a pre-release**；正式版本不勾选。

公开发布 Release 会触发 [publish-npm.yml](../.github/workflows/publish-npm.yml)。工作流核对标签、包版本、锁文件、Release 类型与 `main` 提交，再运行完整检查、打包并独立安装同一份压缩包。

npm 使用 Trusted Publisher，让这个 GitHub 工作流获取短期发布凭据。npm 配置须指向用户 `wangyiliangnet`、仓库 `android-publisher-cli` 和工作流 `publish-npm.yml`，并允许 `npm publish`。仓库不需要 `NPM_TOKEN`。预发布使用 npm 的 `next` 标签，正式版使用 `latest` 标签。

## 确认用户能安装

工作流成功后，查询 npm 注册表并安装刚发布的版本：

```sh
npm view android-publisher-cli versions dist-tags --json
npm install --global android-publisher-cli@VERSION
android-release version --json
```

将 `VERSION` 替换为本次版本。注册表应列出这个版本，`next` 或 `latest` 应指向预期版本，安装后的 CLI 应显示相同版本号。npm 可能先扫描包再开放安装，以注册表和实际安装结果为准。

失败时先查看 Actions 日志、GitHub 标签及 npm 的版本与标签。若 npm 已接受该包，同名版本无法覆盖；修复后准备新版本。若 `main` 在创建 Release 前已有新提交，先评估新增改动是否属于本次发布，再决定是否准备新版本。
