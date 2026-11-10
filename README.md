# CodexPool

**把多个 Codex 账号，放进一个本地工作台。**

CodexPool 是用 Go 编写的本地 Codex 账号管理工具，提供网页控制台、中文终端面板和 CLI。独立登录、查看各账号的额度、选择默认账号，再使用对应账号打开官方 Codex。

[English](README.en.md) · [快速上手](#快速上手) · [命令速查](docs/usage.md) · [Desktop 激活与恢复](docs/desktop.md) · [安全模型](docs/security-model.md) · [兼容性](docs/compatibility.md)

> 当前版本：`0.1.0-preview`，主要面向 macOS。Codex 运行和退出等操作有严格版本门槛，请先阅读下方兼容说明。独立项目，非 OpenAI 官方产品。

## 能做什么

| 功能 | 用途 |
| --- | --- |
| 本地网页控制台 | 在一张表中比较账号、套餐、5 小时/每周额度、其他服务端窗口和 Credits；搜索并查看账号详情 |
| 中文终端面板 | 自适应账号卡片矩阵，支持键盘选择、刷新、查看详情和打开 Codex |
| 独立账号 Profile | 每个账号使用独立 `CODEX_HOME` 和随机目录 ID，名称修改不会移动认证目录 |
| 官方登录流程 | 添加、重新登录和退出通过官方 Codex CLI 完成，无需把 token 粘贴给工具 |
| 默认账号切换 | `switch` 设置 CodexPool 的默认 Profile，`open` 用对应 Profile 打开官方 Codex |
| 额度与缓存 | 自动/手动刷新；读取失败时展示带状态的最后有效快照，未知额度显示“未提供” |
| 保守账号建议 | `best` 根据已验证的新鲜额度给出建议；自动运行与自动轮换暂未启用 |
| Desktop 实验功能 | macOS 独立 helper 支持临时激活账号及恢复原认证状态，需要显式确认 |
| 诊断与脚本接口 | `doctor` 检查环境，部分命令支持 JSON 输出 |

网页只监听本机 `127.0.0.1` 随机端口，前端资源随程序嵌入，无需安装 Node.js。网页与终端面板使用同一套本地 Profile 数据。

## 快速上手

准备 Go **1.24 或更新版本**以及官方 Codex CLI。macOS 会自动识别 ChatGPT 应用内置 Codex；其他安装位置可使用 `CODEXPOOL_CODEX_BIN` 指定。**安装了 Codex 并不意味着版本一定受支持，见兼容说明。**

```bash
git clone https://github.com/GP-BYTE/CodexPool.git
cd CodexPool

# 查看帮助（不会登录或查询账号）
./codexpool.sh --help

# 检查环境；只在独立未登录目录探测 Codex 版本
./codexpool.sh doctor --probe-runtime

# 添加你有权使用的账号，跟随官方登录页面完成授权
./codexpool.sh add work

# 打开网页控制台
./codexpool-web.sh
```

更喜欢终端界面时，运行 `./codexpool.sh`。启动器使用 `go run`，首次运行需要下载 Go 依赖；不会修改 shell 配置或安装到系统 PATH。

网页默认自动打开浏览器；`./codexpool-web.sh --no-open` 可只启动服务。关闭启动终端或按 `Ctrl+C` 即停止服务。

## 日常用法

```bash
./codexpool.sh accounts          # 列出账号
./codexpool.sh show work         # 查看状态和额度
./codexpool.sh switch work       # 设置 CodexPool 默认账号
./codexpool.sh open              # 使用默认账号打开官方 Codex
./codexpool.sh open personal     # 本次使用指定账号
./codexpool.sh relogin work      # 通过官方流程重新登录
./codexpool.sh rename work team  # 修改本地名称
```

`switch` 只改变 CodexPool 默认账号；Desktop 激活是另一项需要确认的实验操作。`add` 省略名称时会使用验证后的邮箱命名。界面会显示已登录账号的邮箱，公开截图前请使用虚构数据或自行遮盖。

终端面板：方向键或 `H/J/K/L` 选择账号，`Enter` 查看详情，`O` 打开 Codex，`U` 设置默认，`R` 全部刷新，`A` 添加，`C` 重新登录，`Q` 退出。`D` 仅取消注册并保留本地数据；**永久删除**使用 `./codexpool.sh delete <名称>`，会先官方退出并要求确认。

更多命令、退出码和环境变量见 [使用说明](docs/usage.md)。

## 兼容说明

| 范围 | 当前行为 |
| --- | --- |
| macOS | Preview 主要目标；网页自动打开浏览器和终端集成面向 macOS |
| Linux / POSIX | 共享部分实现；尚无完整主机兼容矩阵，网页需使用 `--no-open` |
| Windows | 安全权限与可靠锁尚未完成，认证路径主动拒绝执行 |
| 运行、退出等状态变更 | 严格绑定已审查的 Codex `0.154.0-alpha.6.2` |
| 登录与重新登录 | 另接受已审查的确切版本 `0.160.0`；不代表同版本的其他操作受支持 |
| 只读账号 / 额度 | 可尝试其他版本，但响应必须通过严格结构与身份校验；失败时不会接受新数据 |
| Desktop helper | macOS 实验功能，应用打包路径和认证行为有版本限制，不保证任意新版兼容 |

遇到版本不支持时，先运行 `doctor --probe-runtime` 并查阅 [兼容性文档](docs/compatibility.md)。不要通过删除版本检查来绕过限制。

## 数据放在哪里

默认存放在 `~/.codexpool`，可通过 `CODEXPOOL_HOME` 改为专用目录。该目录可能包含官方登录凭据、邮箱、账号标识和额度快照，**不属于项目源码，也不应上传到 GitHub**。不要把它设置为代码仓库根目录。

日常 Profile 命令不会导入或覆盖默认 `~/.codex/auth.json`。Desktop 实验 helper 会临时修改默认认证文件，并在 CodexPool 私有目录保存恢复所需数据；请先阅读 [操作与恢复流程](docs/desktop.md)。

私有目录和文件采用 Unix 权限检查、文件锁、原子写入及身份校验。这些措施不能阻止同 UID 的恶意程序或管理员访问；具体边界见 [安全模型](docs/security-model.md)。

## 构建与贡献

```bash
go mod verify
go test ./...
go test -race ./...
go vet ./...
go build -trimpath -buildvcs=false -o bin/codexpool ./cmd/codexpool
python3 scripts/check_public_export.py
```

构建使用 `-trimpath` 去除本机源码路径。网页的终端操作会调用仓库中的启动器，因此请保留源码目录并从项目根目录启动。

欢迎提交问题与改进。请阅读 [贡献指南](CONTRIBUTING.md)、[安全报告说明](SECURITY.md) 和 [发布流程](docs/releasing.md)。提交 Issue 时请勿附上认证目录、真实账号快照或未经检查的日志。

项目使用 [MIT License](LICENSE)。协议证据与依赖的来源见 [第三方说明](THIRD_PARTY_NOTICES.md)。
