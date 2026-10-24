# CLI 版本发布

`android-publisher-cli` 通过版本 PR 和 GitHub Release 发布到 npm 注册表。先按 [npm 包验收](npm-package.md)检查交付物。CLI 发版与 Android 应用提审分别授权。

## 准备版本 PR

在 GitHub Actions 打开 [Prepare npm release](../.github/workflows/prepare-npm-release.yml)。从 `main` 运行工作流，输入高于当前版本的 SemVer，不带 `v` 和构建元数据。工作流更新 `package.json` 与 `package-lock.json`，并创建版本 PR。

审阅版本号和包文件清单。由 `GITHUB_TOKEN` 创建的 PR 可能需要维护者在 GitHub 上批准 CI 运行。等待该 PR 的必需 `check` 通过，再按仓库规则 squash 合并。

## 发布 GitHub Release

确认合并后 `main` 顶端提交的必需 `check` 通过。在 GitHub Releases 中建立 `vVERSION`，目标选该提交。发布说明写明用户可见变化、Node.js 要求、状态兼容性、已知限制，以及六个渠道的真实账号验证情况。

版本带预发布后缀时，勾选“Set as a pre-release”；正式版本不勾选。公开 GitHub Release 会触发 [publish-npm.yml](../.github/workflows/publish-npm.yml)。工作流核对标签、包版本、锁文件、Release 类型和 `main` 提交，再运行完整检查、打包并独立安装同一份 tarball。

工作流通过 npm Trusted Publisher 取得短期发布凭据。npm 配置须指向 GitHub 用户 `wangyiliangnet`、仓库 `android-publisher-cli` 和工作流文件 `publish-npm.yml`。配置中须允许 `npm publish`；仓库不需要 `NPM_TOKEN`。预发布版本使用 `next` 标签，正式版本使用 `latest`。

## 核对与失败处理

工作流成功后，查询注册表并安装指定版本：

```sh
npm view android-publisher-cli versions dist-tags --json
npm install --global android-publisher-cli@VERSION
android-release version --json
```

将 `VERSION` 换成刚发布的版本。npm 可能先处理包扫描，再开放安装；以注册表中的版本和安装结果为准。

发布失败时，先查看 Actions 日志、GitHub 标签和 npm 中的版本与标签。若包已被 npm 接受，同名版本不能覆盖；修复内容须使用新版本号。若 `main` 在创建 Release 前已经前进，先评估新增改动，再准备新版本。

CI 和 npm 发布检查不证明真实商店账号权限，也不授权应用提审。
