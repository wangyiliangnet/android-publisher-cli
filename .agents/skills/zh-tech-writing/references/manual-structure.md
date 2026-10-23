# 产品手册结构与文件名

写一整套软件手册或文档站时参考。

## 目录结构

| 部分 | 英文名 | 必备 | 形式 | 内容 |
|---|---|---|---|---|
| 简介 | Introduction | 是 | 文件 | 产品和文档的总体说明 |
| 快速上手 | Getting Started | 否 | 文件 | 最快用起来的办法 |
| 入门篇 | Basics | 是 | 目录 | 初级使用教程 |
| ├ 环境准备 | Prerequisite | 是 | 文件 | 使用前要满足的条件 |
| ├ 安装 | Installation | 否 | 文件 | 安装方法 |
| └ 设置 | Configuration | 是 | 文件 | 配置说明 |
| 进阶篇 | Advanced | 否 | 目录 | 中高级开发教程 |
| API | Reference | 否 | 目录或文件 | 逐个介绍 API |
| FAQ | FAQ | 否 | 文件 | 常见问题 |
| 附录 | Appendix | 否 | 目录 | 名词解释（Glossary）、最佳实践（Recipes）、故障处理（Troubleshooting）、版本说明（ChangeLog）、反馈方式（Feedback） |

参考范例：[Redux 文档](https://redux.js.org/introduction/getting-started)。

## 文件名

- 只用半角字符，不用中文，不含空格。
- 用小写字母。`README`、`LICENSE` 这类说明文件可以大写。
- 多个单词用 `-` 连接。

```
差：名词解释.md    好：glossary.md
差：TroubleShooting.md    好：troubleshooting.md
差：advanced_usage.md    好：advanced-usage.md
```
