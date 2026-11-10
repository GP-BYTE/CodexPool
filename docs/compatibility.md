# CodexPool v0.1 兼容性边界

日期：2026-09-08

## 1. 固定证据基线

v0.1 的**交互/状态变更路径**以仓库已经审查的 Codex **0.154.0-alpha.6.2** 为基线：

- 研究日期：2026-09-13；
- macOS 证据二进制：`/Applications/ChatGPT.app/Contents/Resources/codex`；
- 证据来源：本机官方 binary 在全新未登录 `CODEX_HOME` 中生成的 TypeScript protocol schema；
- evidence manifest SHA-256：`32b2700f2922f7aaf88c39feec2d556dc2e5de5a6e997d61d0842794d645a029`。

运行前仍使用独立、未登录的 runtime-probe `CODEX_HOME` 执行 `codex --version`。但兼容策略现在按风险分层：

- `run/logout` 等交互和状态变更操作继续要求 runtime 与 pinned evidence **完全一致**；官方 `login` / 重新登录额外接受已检查的确切版本 **0.160.0**，见 [登录兼容证据](evidence/codex-0.160.0-login/README.md)，未知版本保持 fail closed；
- 只读账号/额度查询不再仅因版本字符串变化就提前拒绝。它允许启动短生命周期 App Server，再由 `account/read` / `account/rateLimits/read` 的严格响应结构校验决定新协议是否可接受；
- 若新版 App Server 的响应已经不符合当前受支持 schema，本次新数据 fail closed，并显示协议兼容提示；最后一次已经通过 CodexPool 自身规范化与身份绑定校验的历史快照仍可展示，不因 ChatGPT App 自动升级而把整个账号矩阵清空；
- cache 中的 `CodexVersion` 是数据来源 provenance，不再作为读取 CodexPool 自身规范化快照的唯一兼容门槛。

## 2. 0.154.0-alpha.6.2 协议假设

当前 DTO/查询依据仓库 `docs/evidence/codex-0.154.0-alpha.6.2/` 与保留的 `codex-0.153.4/` 共同类型：

- `account/read` 使用 `refreshToken:false`；
- quota 读取使用 `account/rateLimits/read`；
- `rateLimitsByLimitId` 非空 map 优先，legacy `rateLimits` 仅 fallback；
- 300 分钟只识别为 5H，10080 分钟只识别为 Weekly；
- unknown/null 保持 unknown，不转换成 0 或 100；
- credits balance 保持 opaque string；
- `ordinaryUsageAllowed` 与 `normalModelSlug` 必须存在且仅接受其生成 schema 所允许的类型；
- `account/rateLimits/read` 的新增可选参数保持省略，不主动声明 reserve support 或跳过 reset-credit 详情。

因此 v0.1 的 `auto` 继续保持关闭。`best` 只能提供保守排序并标记 uncertainty，不会据 `ordinaryUsageAllowed` 自动运行、轮换账号或消耗 reset credits。

## 3. 凭据存储

- **file store**：v0.1 唯一允许 authenticated 使用的 store；由每个独立 `CODEX_HOME/config.toml` 指定并由官方 Codex 管理。
- **keyring**：数据模型保留，但 CLI/authflow/runtime 认证路径 fail closed，直到完成 macOS/Linux/Windows 的命名空间、删除和账号串用验证。

## 4. 平台

- macOS/POSIX：当前 Preview 的主要目标；Unix owner/nofollow/flock/process-group 路径有 synthetic/race 回归。
- Linux/BSD：共享 POSIX build-tag 路径，但当前没有同等发布级主机矩阵证据。
- Windows：owner/DACL、可靠 lock、process-tree/job-object 语义未完成，authenticated path fail closed；Windows stub 不代表安全等价实现。
- ChatGPT Desktop：保留需要显式确认的 macOS 实验性激活/恢复 helper；会临时修改默认认证文件，不能据此承诺任意应用版本兼容。见 [Desktop 使用说明](desktop.md)。

## 5. Go 与 UI 依赖

模块当前声明 Go `1.24.0`，UI 固定：

- Bubble Tea `v1.3.10`
- Lip Gloss `v1.1.0`

发布验证请参见 [验证说明](verification.md)。合成测试不等于真实账号或跨平台兼容性验证。

## 6. 升级 Codex 的流程

看到新的 Codex 版本时不要直接放宽 gate：

1. 在不读取真实账号的独立环境记录新 binary/version；
2. 更新并哈希协议 evidence；
3. 对 account/read、rateLimits/read、notification、credential-store 与 logout 行为重新做源码/协议核对；
4. 更新 wire DTO 与 synthetic payload；
5. 运行 focused、full、race、vet、build；
6. 只有新版本 evidence 与回归全部闭合后，才调整 runtime version gate；
7. 真实账号行为仍需用户针对具体账号另行授权。

未知字段、未知 credential-store 行为、未知平台权限或新 App Server 行为一律保持 unknown/fail closed，而不是从“新版本大概率兼容”推断可用。
