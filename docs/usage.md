# 使用说明

从项目根目录运行启动器；也可以构建 CLI 后使用同名子命令。首次运行需要下载 Go 依赖。

## 三种入口

```bash
./codexpool.sh                   # 交互终端中打开中文 TUI
./codexpool-web.sh               # 本机网页并自动打开浏览器（macOS）
./codexpool-web.sh --no-open     # 只打印网页地址
./codexpool.sh --help
```

网页的账号操作需要官方交互时，会打开终端继续确认；自动打开失败时会提供可复制命令。额度在打开界面后刷新，界面活动时约每 60 秒再次刷新，也可以手动触发。

## 账号管理

```bash
./codexpool.sh add work
./codexpool.sh add personal --device-auth
./codexpool.sh accounts
./codexpool.sh show work
./codexpool.sh switch work
./codexpool.sh open
./codexpool.sh open personal
./codexpool.sh rename work team
./codexpool.sh relogin team
./codexpool.sh logout team
./codexpool.sh remove team       # 仅取消注册，保留本地数据
./codexpool.sh delete personal   # 官方退出成功后永久删除，需确认
```

仅添加本人有权使用的账号。省略名称会采用验证后的账号邮箱；名称是元数据，不参与物理目录命名。`--device-auth` 的可用性由官方 CLI 和账号设置决定。

`add`、`relogin`、`logout`、`delete` 和 `remove --purge` 默认要求确认。`--yes` 仅用于该次调用的明确授权。

## 查询与脚本

```bash
./codexpool.sh list --json
./codexpool.sh status --all --json
./codexpool.sh status work --cached
./codexpool.sh doctor --json
./codexpool.sh doctor --probe-runtime
./codexpool.sh best --json --refresh
./codexpool.sh run work -- --help
./codexpool.sh version
```

JSON、诊断输出和截图可能含邮箱、Profile ID 或本机路径，分享前需要检查。`best` 只提出保守建议，`auto` 当前禁用。

`run` 会拒绝改变认证、配置或 provider 路由边界的参数，例如 `--config`、`--profile` 和 `--remote`。未知或不受支持的 Codex 版本也可能被拒绝，参见 [兼容性](compatibility.md)。

## 环境变量

| 变量 | 含义 |
| --- | --- |
| `CODEXPOOL_HOME` | 独立私有数据目录，默认为 `~/.codexpool`；不要指向仓库或已有无关目录 |
| `CODEXPOOL_CODEX_BIN` | 显式指定可信的官方 Codex 可执行文件路径 |
| `NO_COLOR` | 禁用终端颜色 |

## 额度解释

5 小时窗口只对应服务端的 300 分钟，每周窗口只对应 10080 分钟。其他窗口单独显示。剩余百分比来自 `100 - usedPercent` 并约束在 0–100；缺失数据不会被解释为 0 或 100。Credits 原样展示，不推断货币或 token 单位。

若提示“需重新登录”，使用 `relogin` 或 TUI 的 `C`，继续刷新无法修复失效登录。缓存仅代表上次有效快照，不保证实时额度。

## 退出码

| 退出码 | 含义 |
| --- | --- |
| 0 | 成功 |
| 2 | 参数或用法错误 |
| 3 | 未认证或 Profile 身份/代际已失效 |
| 4 | 不支持的运行时、平台或凭据存储 |
| 5 | RPC 或运行时错误 |
| 6 | Profile 忙 |

`open/run` 保留正常 Codex 子进程退出码；Unix 信号退出映射为 `128 + signal`。
