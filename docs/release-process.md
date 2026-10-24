# 维护者：版本发布

本流程将 CLI 发布到 npm 注册表，不会向 Android 商店提交应用。构建与检查命令见[构建与分发](mac-cli-delivery.md)。

## 准备版本

在 GitHub Actions 的 [Prepare npm release](../.github/workflows/prepare-npm-release.yml) 页面，从 `main` 运行工作流。输入更高的 SemVer（不带 `v`）和一行公开更新说明。当前 `0.2.0-dev` 的下一个正式版本可填 `0.2.0`。

工作流会更新 `package.json`、`package-lock.json` 和 [CHANGELOG.md](../CHANGELOG.md)，并创建版本 PR。不能重复使用已有版本号。

审阅 PR 中的版本号和更新记录。按实际变化补充状态兼容性、已知限制和安装要求。等待 PR 的必需 CI `check` 通过后再合并。

由 `GITHUB_TOKEN` 创建的 PR 可能需要维护者在 GitHub 上批准工作流运行。

## 发布到 npm

版本 PR 合并后，确认 `main` 顶端提交的 CI `check` 通过。在 GitHub Releases 中建立 `vVERSION`，目标选当前 `main` 顶端提交。说明中列出 Node.js 要求、状态兼容性和六个渠道的真实账号验证情况。

预发布版本（如 `0.2.0-rc.1`）须勾选“Set as a pre-release”；正式版本不要勾选。发布 GitHub Release 后，[publish-npm.yml](../.github/workflows/publish-npm.yml) 会核对标签、包版本、锁文件和 `main` 提交。核对通过后，工作流运行完整检查，并发布已检查的 tarball。若 `main` 已前进，先评估新增改动并使用新版本号。

工作流成功后，在 npm 上核对 `android-publisher-cli@VERSION` 和对应标签。安装该版本，运行 `android-release version --json`，检查包内文档。

发布失败时先看 Actions 日志和 npm 中是否已有同版本。npm 已接受的版本不能覆盖，修复内容须使用新版本号。

首次发布前，在 npm 账号中为包 `android-publisher-cli` 配置 Trusted Publisher。填写 GitHub 用户 `wangyiliangnet`、仓库 `android-publisher-cli` 和工作流文件名 `publish-npm.yml`，并允许 `npm publish`。工作流使用 OIDC 短期凭据，不需要 `NPM_TOKEN`。

新建 Trusted Publisher 后，应在 2 天内完成首次发布；过期后需删除并重建配置。GitHub 仓库的 Actions 设置还须允许工作流创建 PR。版本工作流需要 `contents: write` 和 `pull-requests: write` 权限。

PR 和 npm 发布检查只说明对应代码与包通过测试，不代表真实商店账号已验证，也不授权应用提审。
