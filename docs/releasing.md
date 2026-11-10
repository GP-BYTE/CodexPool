# 发布流程

## 源码版本

1. 在不包含真实账号数据的目录准备源码候选。
2. 运行 [贡献指南](../CONTRIBUTING.md) 中的格式、测试、race、vet、构建及隐私检查。
3. 检查所有 Git 暂存文件及历史；忽略规则不会清除已跟踪的敏感文件。
4. 确认作者/提交者不含个人姓名与私人邮箱，截图仅含虚构数据。
5. 推送审查后的提交，使用与 `internal/cli/app.go` 一致的版本标签，例如 `v0.1.0-preview.1`。
6. Release 说明列出功能、版本限制、验证范围及源码校验和；不附原始日志或真实账号截图。

首次公开导出使用全新 Git 历史，作者与提交者均为 `CodexPool contributors <contributors@example.invalid>`。公开仓库的所属账号由 GitHub 显示；源码脱敏不会隐藏仓库所属账号。

## GitHub 首页

建议简介：`Local Codex account manager with a web console, terminal dashboard, isolated profiles, and usage monitoring.`

建议 Topics：`codex`、`codex-cli`、`account-manager`、`golang`、`tui`、`local-first`、`macos`。首页由中英文 README 提供，避免使用真实账号截图。不要把文档里的 `GP-BYTE` 仓库路径误当成私有账号登录信息；它是明确指定的公开仓库命名空间。

建议启用私密漏洞报告与依赖安全提醒。CI 仅执行合成测试，不配置真实 Codex 登录凭据。

## 二进制版本

首次导出仅发布源码。后续发布二进制前，使用 `-trimpath -buildvcs=false` 构建，检查产物元数据、字符串和全部依赖许可，并在实际目标平台验证。不要上传普通 `go build` 产生的开发二进制或整个本地目录。
