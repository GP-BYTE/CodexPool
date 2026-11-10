# Contributing

欢迎改进账号管理、交互体验、协议兼容和测试。请先阅读 [兼容性](docs/compatibility.md) 与 [安全模型](docs/security-model.md)。

## 开发与验证

使用 Go 1.24 或更新版本。从项目根目录运行：

```bash
gofmt -w cmd internal
go mod verify
go test ./...
go test -race ./...
go vet ./...
go build -trimpath -buildvcs=false -o bin/codexpool ./cmd/codexpool
python3 scripts/check_public_export.py
```

测试应使用临时目录、虚构身份与模拟 Codex/App Server。不要让常规测试登录真实账号、读取已有认证文件、消耗额度或修改 Desktop 登录。

## 提交规范

- PR 说明具体问题、修改后的行为及验证结果。
- 更新协议时提供无认证环境生成的类型证据，保留来源和文件哈希；不要仅放宽版本门槛。
- 任何新增共享状态都要考虑 Profile ID、auth generation、锁和原子写入。
- 所有样例使用 `example.invalid` / `example.com` 邮箱和虚构路径。
- 提交前检查源码、文档、图片、日志以及 Git 作者邮箱；请使用 GitHub 提供的 noreply 邮箱或项目认可的匿名身份。

不要提交 `~/.codexpool`、`~/.codex`、真实 token、邮箱列表、账号快照或恢复数据。安全问题请按 [SECURITY.md](SECURITY.md) 报告。
