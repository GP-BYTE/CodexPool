# 协议证据：codex-cli 0.153.4

生成日期：2026-09-07。使用本机官方应用携带的 Codex，在全新临时 CODEX_HOME 中执行：

```text
codex app-server generate-ts --out <temporary-schema-directory>
```

这里只保留账号读取、登录、额度响应/通知及其类型依赖，共 19 个生成文件。文件保持生成器原样，SHA-256 见 [manifest.json](manifest.json)。这些文件用于审阅协议，不是已实现的 TypeScript SDK，也不是账号响应样本。

生成时没有读取现有账号凭据，没有登录、发送额度请求或运行模型任务。真实账号返回值、套餐可用性、Token refresh 和多进程隔离仍需集成测试。

研究所查官方 main 提交为 `e1eb98461cd42730e7b1a890c3100303d7091bc2`。不要将这些本机生成文件标记为该 main 提交构建产物；两者不同。

主要差异：

- 0.153.4 的 `GetAccountRateLimitsResponse` 没有 main 中的 `ordinaryUsageAllowed`。
- 0.153.4 的 `RateLimitSnapshot` 没有 main 中的 `normalModelSlug`。
- 0.153.4 的生成 `ClientRequest` 把 `account/rateLimits/read` 参数定义为 `undefined`；main 已新增可选参数结构。
- 0.153.4 已包含 `accountId`、`individualLimit`、`spendControlReached` 和 earned reset credits 类型。

主文档中的所有字段须按“官方文档 / 固定源码 / 本机生成类型 / 真实账号实测”分别理解。
