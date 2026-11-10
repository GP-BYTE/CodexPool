# CodexPool v0.1 安全模型

日期：2026-09-08

本文描述 v0.1 Preview 实际实现的安全边界。目标是隔离**用户本人明确授权**的多个 Codex Profile，降低账号串用、凭据误导入、跨 Profile 状态污染和本地恢复错误；它不是操作系统安全沙箱，也不承诺账号平台“零风控/零限制”。

## 1. 受保护资产与信任边界

主要资产是每个 Profile 的官方 Codex 管理状态、身份绑定、用量快照和运行互斥关系。CodexPool 信任用户选择的本机官方 Codex 可执行文件及操作系统基本文件权限；不信任 alias、缓存内容、registry 内容、终端动态文本、未知协议字段或未经验证的 runtime 版本。

CodexPool **不**把当前 ChatGPT/Codex Desktop 登录当作授权来源，也不扫描默认 `~/.codex`、浏览器 cookie、Keychain 或其它账号目录来“自动发现”账号。

## 2. Profile 与认证隔离

- Profile 使用随机不可变 `p_<32hex>` 物理 ID；alias 只用于显示和解析。
- 每个 Profile 有独立 `CODEX_HOME`；rename 不移动物理目录。
- v0.1 authenticated Profile 只允许官方 Codex 的 file credential store；keyring 在完成平台隔离/删除语义验证前 fail closed。
- OAuth login/logout 与 token refresh 由官方 Codex CLI 完成。CodexPool 不复制 token、不自行 refresh、不覆盖全局 `auth.json`。
- 用量只通过该 Profile 自己的短生命周期官方 App Server 读取：`account/read(refreshToken:false)` 与 `account/rateLimits/read`。
- 只读账号/额度路径允许在官方 Codex 版本变化后继续尝试读取，但不会盲信新版本：返回 payload 必须继续通过当前严格结构校验，身份仍须与 Profile 绑定一致；不兼容的新 payload fail closed。交互、退出等状态变更路径仍要求 runtime 与已审查 evidence 完全匹配；官方登录额外允许已检查的确切版本 0.160.0，未知版本继续拒绝，Profile 隔离、互斥和身份校验不变。
- 已写入 cache 的 quota 是 CodexPool 自身规范化数据；只要其结构、Profile generation 和已知 account identity 仍通过校验，可以跨官方 Codex 升级继续作为“最后一次有效快照”展示，且保留原始 `CodexVersion` provenance。
- 身份与已记录 email/backend account id 不一致时 fail closed：generation 前进、Profile 进入 `reauthRequired`、旧快照不再复用。

## 3. 文件系统边界

- 私有目录目标权限 `0700`，私有文件/配置/cache/lock 目标权限 `0600`。
- Unix 私有文件读取和 lock 使用 no-follow descriptor；验证当前 UID、regular file 与 link count，并拒绝 symlink/hard-link 私有文件。
- `CODEXPOOL_HOME` 首次创建时写 `.codexpool-managed-root`。已有无关目录不会被静默收编或 chmod；只允许合法 marker 或可识别的旧版 CodexPool 根迁移。
- Store 固定解析已存在的根目录祖先，降低初始化前后祖先 symlink 改指风险。
- Profile purge 通过 `os.Root` 锚定 `accounts/` 后递归删除；验证后的祖先若被换成指向根外的 symlink，删除 fail closed。

Atomic write 使用：同目录 temp → 私有权限 → write → file sync → close → rename → parent-directory sync。错误分为：

- rename 前失败：`CommitNotCommitted`，旧目标保持；
- 全部完成：`CommitCommitted`；
- rename 已可见但 parent-directory sync 失败：`CommitDurabilityUncertain`，调用方必须按“新值可能已提交”恢复，不能假装回滚。

## 4. 锁与状态转换

Profile query/login/logout/run/purge 使用持久 lock 文件上的 OS `flock`。CodexPool 正常 release/reacquire 不 unlink lock，Profile purge 也不删除 `locks/profile-*.lock`，避免自身创建同名不同 inode 的两把锁。

当前锁顺序：

1. login：global-login lock → Profile lock → registry lock；
2. logout/remove/query/run：Profile lock → 必要时 registry lock；
3. registry transaction callback 不反向获取 Profile/global lock。

当前实现没有已知的 registry → Profile/global 反向锁序。

限制：Unix `flock` 是 advisory。与 CodexPool 同一 UID 的外部恶意进程如果主动 unlink/替换 lock 文件，可以破坏 pathname 对单一 inode 的约定；v0.1 不声称能在同 UID 恶意进程模型下提供强制互斥。root/管理员同样超出该保证。

## 5. 子进程与 App Server 生命周期

- Unix App Server 在独立 process group 中启动。
- Close 先关闭 stdin；正常退出后仍清理 owned process group，覆盖继承同一 Profile 环境的 helper/descendant。
- timeout 时关闭 RPC 并向整个 owned process group 发送 SIGKILL，再回收 direct child。
- Profile lease 在 owned process、RPC/pipe resource finalize 完成前保持占用；清理未确认时 fail closed。
- Close 使用 `sync.Once`；synthetic 回归覆盖 8 个并发调用者共享一次 cleanup。
- Unix 交互 `run/login/logout` 在 context 取消时先发送 SIGINT，提供 2 秒 graceful window，之后才允许 `exec` fallback kill。
- `codexpool run` 对正常退出保留 child exit code；Unix signal termination 映射为 `128 + signal`。

## 6. RPC 边界

- JSONL inbound/outbound frame 有大小上限；short write、truncated frame、result+error、duplicate response 等异常 fail closed。
- server-initiated request 一律以 `-32601` 拒绝，不执行服务端要求的任意动作。
- 通知是 advisory；burst 可丢弃而不阻塞 response，后续完整 read 才是事实来源。
- 已缓冲完整 response 与 cancellation/Close 同时 ready 时优先交付完整 response。
- 远端 `RPCError.Message/Data` 不直接拼入用户可见错误字符串；本地 transport/protocol failure 仍保留必要诊断原因。

## 7. 环境与本机攻击边界

Codex 子进程环境会去除已知认证、provider、endpoint/routing override，并明确设置唯一 Profile `CODEX_HOME`。`run` 阻止已知能绕过 Profile 配置/认证边界的 config/profile/provider/remote 参数。

这不是通用 hostile-environment sandbox。若官方 Codex 二进制被恶意替换、父进程注入尚未识别的新型敏感环境变量，或攻击者拥有同 UID/root 权限，v0.1 不能提供强隔离保证。

## 8. 真实账号授权边界

网页控制台只监听随机的 `127.0.0.1` 端口，使用进程内随机会话密钥、精确 Host/Origin 校验和不缓存响应。页面初次读取已验证的本地缓存；用户打开后与 TUI 一样可刷新独立 Profile 的官方用量。网页设置默认账号时重新验证不可变 ID、auth generation 和 lifecycle。需要终端交互的操作在用户点击确认后打开独立终端窗口，继续走现有 CLI/官方 Codex 流程；网页没有通用命令执行接口，也不接收凭据或 OAuth code。同一 UID 的本机进程仍超出网页进程的安全隔离保证。

开发 CodexPool 不等于授权访问账号。未经用户针对具体账号明确授权，禁止：

- 启动该账号官方 OAuth；
- 读取、复制或导入任何已有凭据；
- 查询真实 quota/rate limits/credits；
- 运行真实 Codex 任务；
- 执行多账号真实并发矩阵。

真实验收也只能报告观察到的行为，不能推出“官方 API/CLI = 零封禁、零限制、零风控”。
