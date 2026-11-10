package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"codexpool/internal/authflow"
	"codexpool/internal/codexruntime"
	"codexpool/internal/diagnostics"
	"codexpool/internal/profile"
	"codexpool/internal/securefs"
	"codexpool/internal/textsafe"
	"codexpool/internal/usage"
)

const Version = "0.1.0-preview"

const helpText = `CodexPool — 多账号隔离管理工具

用法：
  请先进入 CodexPool 项目目录，再执行下面的命令。

快速开始（推荐）：
  ./codexpool.sh                         打开账号监控面板
  ./codexpool-web.sh                     打开本机网页控制台
  ./codexpool.sh add [名称]              添加/登录账号；省略名称时按验证后的官方邮箱自动命名
  ./codexpool.sh accounts                查看全部账号
  ./codexpool.sh show [名称]             查看状态/额度；省略名称时查看默认账号
  ./codexpool.sh switch <名称>           设为 CodexPool 默认账号
  ./codexpool.sh open [名称]             使用指定账号打开 Codex；省略时使用默认账号
  ./codexpool.sh delete <名称>           官方退出后永久删除该 Profile；执行前再次确认

快捷别名：
  accounts / ls  -> list
  show / info    -> status
  switch         -> use
  open           -> run
  delete         -> remove --purge

高级 / 兼容命令：
  ./codexpool.sh list [--json]
  ./codexpool.sh status [<alias>] [--all] [--json] [--cached]
  ./codexpool.sh use <alias>
  ./codexpool.sh run [<alias>] -- [codex args...]
  ./codexpool.sh relogin <alias> [--device-auth] [--yes]
  ./codexpool.sh logout <alias> [--yes]
  ./codexpool.sh rename <alias> <new-alias>
  ./codexpool.sh remove <alias> [--purge] [--yes]
  ./codexpool.sh doctor [--json] [--probe-runtime]
  ./codexpool.sh tui
  ./codexpool.sh web [--no-open]
  ./codexpool.sh best [--json] [--refresh]
  ./codexpool.sh auto  （v0.1 preview 中主动禁用）
  ./codexpool.sh version

安全说明：
  每个 Profile 都使用不可变随机 ID、独立私有目录和独立 CODEX_HOME。
	日常 Profile 命令不复制 auth.json，不在 Profile 之间复制 token，也不自行刷新 OAuth token。
	登录/退出均委托官方 Codex CLI；默认 ~/.codex 登录不会被导入或覆盖。
	实验性的 macOS Desktop helper 与日常命令/TUI 完全分离，只能在用户明确授权的单账号人工测试中使用。
  v0.1 的认证 Profile 仅允许 file store；keyring 在完成平台隔离验证前继续 fail closed。
  运行时严格绑定本项目已验证的 Codex 0.154.0-alpha.6.2 协议证据。
`

const (
	ExitOK             = 0
	ExitUsage          = 2
	ExitNotLoggedIn    = 3
	ExitUnsupported    = 4
	ExitRuntimeFailure = 5
	ExitBusy           = 6
)

type App struct {
	Store            *profile.Store
	Runtime          *codexruntime.Runtime
	Usage            *usage.Service
	Auth             *authflow.Service
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
	EvidenceManifest string
}

func NewDefault() (*App, error) {
	return newDefault(codexruntime.DefaultBinary)
}

func newDefault(resolveBinary func() (string, error)) (*App, error) {
	root, err := securefs.DefaultRoot()
	if err != nil {
		return nil, err
	}
	store, err := profile.NewStore(root)
	if err != nil {
		return nil, err
	}
	binary, discoveryErr := resolveBinary()
	var rt *codexruntime.Runtime
	if discoveryErr != nil {
		rt, err = codexruntime.NewUnavailable(store, discoveryErr)
	} else {
		rt, err = codexruntime.New(binary, store)
	}
	if err != nil {
		return nil, err
	}
	usageService := usage.NewService(rt, Version, diagnostics.BuiltInEvidence.CodexVersion)
	app := &App{
		Store:   store,
		Runtime: rt,
		Usage:   usageService,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
	app.Auth = authflow.New(store, rt, usageService, app.Stdin, app.Stdout, app.Stderr)
	app.EvidenceManifest = discoverEvidenceManifest()
	return app, nil
}

func RunStatic(args []string, stdout io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, helpText)
		return ExitOK, true
	case "version":
		fmt.Fprintf(stdout, "codexpool %s\n", Version)
		return ExitOK, true
	default:
		return 0, false
	}
}

func (a *App) Run(ctx context.Context, args []string) int {
	if code, handled := RunStatic(args, a.Stdout); handled {
		return code
	}
	if len(args) == 0 {
		if writerIsTTY(a.Stdout) {
			return a.commandTUI(ctx, nil)
		}
		a.printHelp()
		return ExitOK
	}

	command, commandArgs := normalizeTopLevelCommand(args[0], args[1:])
	switch command {
	case "help", "-h", "--help":
		a.printHelp()
		return ExitOK
	case "version":
		fmt.Fprintf(a.Stdout, "codexpool %s\n", Version)
		return ExitOK
	case "list":
		return a.commandList(commandArgs)
	case "status":
		return a.commandStatus(ctx, commandArgs)
	case "use":
		return a.commandUse(commandArgs)
	case "run":
		return a.commandRun(ctx, commandArgs)
	case "add":
		return a.commandAdd(ctx, commandArgs)
	case "relogin":
		return a.commandRelogin(ctx, commandArgs)
	case "logout":
		return a.commandLogout(ctx, commandArgs)
	case "rename":
		return a.commandRename(commandArgs)
	case "remove":
		return a.commandRemove(ctx, commandArgs)
	case "doctor":
		return a.commandDoctor(ctx, commandArgs)
	case "tui":
		return a.commandTUI(ctx, commandArgs)
	case "web":
		return a.commandWeb(ctx, commandArgs)
	case "best":
		return a.commandBest(ctx, commandArgs)
	case "auto":
		return a.commandAuto(ctx, commandArgs)
	default:
		return a.fail(ExitUsage, fmt.Errorf("未知命令 %q；可执行 ./codexpool.sh --help 查看用法", args[0]))
	}
}

func normalizeTopLevelCommand(command string, args []string) (string, []string) {
	forwarded := append([]string(nil), args...)
	switch command {
	case "accounts", "ls":
		return "list", forwarded
	case "show", "info":
		return "status", forwarded
	case "switch":
		return "use", forwarded
	case "open":
		return "run", forwarded
	case "delete":
		return "remove", append(forwarded, "--purge")
	default:
		return command, forwarded
	}
}

func (a *App) fail(code int, err error) int {
	code = normalizeExitCode(code, err)
	if err != nil {
		fmt.Fprintln(a.Stderr, "codexpool:", textsafe.Clean(err.Error(), 800))
	}
	return code
}

func normalizeExitCode(code int, err error) int {
	if err == nil {
		return code
	}
	switch {
	case errors.Is(err, codexruntime.ErrUnsupportedRuntime), errors.Is(err, codexruntime.ErrUnsupportedCredentialStore):
		return ExitUnsupported
	case errors.Is(err, securefs.ErrBusy):
		return ExitBusy
	case errors.Is(err, profile.ErrNotReady), errors.Is(err, profile.ErrProfileChanged),
		errors.Is(err, usage.ErrNotAuthenticated), errors.Is(err, usage.ErrNotChatGPT), errors.Is(err, usage.ErrIdentityChanged):
		return ExitNotLoggedIn
	default:
		return code
	}
}

func (a *App) printHelp() {
	fmt.Fprint(a.Stdout, helpText)
}

func writerIsTTY(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func discoverEvidenceManifest() string {
	path := filepath.Join("docs", "evidence", "codex-0.154.0-alpha.6.2", "manifest.json")
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		abs, err := filepath.Abs(path)
		if err == nil {
			return abs
		}
	}
	return ""
}
