# 协议证据：codex-cli 0.154.0-alpha.6.2

生成日期：2026-09-13。使用本机官方 ChatGPT 应用携带的 Codex，且在全新临时、未登录的 `CODEX_HOME` 中执行：

```text
codex app-server generate-ts --out <temporary-schema-directory>
```

本目录保留相对于 `0.153.4` 的账号/额度协议变化及新增额度读取参数。生成过程中没有读取任何现有认证文件、没有登录、没有发送账号或额度请求，也没有运行模型任务。完整的旧版共同类型仍保留在相邻的 `codex-0.153.4` evidence 中。

`GetAccountRateLimitsResponse` 新增必填的 `ordinaryUsageAllowed`；每个 `RateLimitSnapshot` 新增必填的 `normalModelSlug`；`account/rateLimits/read` 新增可选参数，但 CodexPool 保持发送无参数请求。`auto` 仍然禁用，不会据这个新增字段自动执行或轮换账号。

文件和官方二进制 SHA-256 见 [manifest.json](manifest.json)。本证据仅覆盖本机 macOS arm64 的生成 schema 与无认证 App Server 启动，不是任何真实账号测试。
