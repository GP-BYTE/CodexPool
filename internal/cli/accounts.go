package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codexpool/internal/codexruntime"
	"codexpool/internal/profile"
	"codexpool/internal/textsafe"
)

func (a *App) commandUse(args []string) int {
	if len(args) != 1 {
		return a.fail(ExitUsage, errors.New("用法：./codexpool.sh use <账号名称>"))
	}
	p, err := a.Store.Resolve(args[0])
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	return a.useExpected(p)
}

func (a *App) useExpected(p profile.Profile) int {
	if p.Lifecycle != profile.LifecycleReady {
		return a.fail(ExitNotLoggedIn, fmt.Errorf("账号 %q 当前状态为 %s，尚未就绪", p.Alias, displayState(string(p.Lifecycle))))
	}
	updated, err := a.Store.SetActiveExpected(p)
	if err != nil {
		return a.fail(ExitRuntimeFailure, err)
	}
	fmt.Fprintf(a.Stdout, "CodexPool 默认账号已切换为：%s\n", textsafe.Clean(updated.Alias, 63))
	fmt.Fprintln(a.Stdout, "这不会改变当前 ChatGPT/Codex Desktop 的登录状态。")
	return ExitOK
}

func (a *App) commandRun(ctx context.Context, args []string) int {
	before, forwarded := splitDoubleDash(args)
	if len(before) > 1 {
		return a.fail(ExitUsage, errors.New("用法：./codexpool.sh run [<账号名称>] -- [Codex 参数...]"))
	}

	var p profile.Profile
	var err error
	if len(before) == 1 && before[0] != "" {
		p, err = a.Store.Resolve(before[0])
	} else {
		p, err = a.Store.Active()
	}
	if err != nil {
		return a.fail(ExitUsage, fmt.Errorf("无法解析要启动的账号：%w", err))
	}
	return a.runExpected(ctx, p, forwarded)
}

func (a *App) runExpected(ctx context.Context, p profile.Profile, forwarded []string) int {
	if p.Lifecycle != profile.LifecycleReady {
		return a.fail(ExitNotLoggedIn, fmt.Errorf("账号 %q 当前状态为 %s，尚未就绪", p.Alias, displayState(string(p.Lifecycle))))
	}
	if err := preflightExecRepository(forwarded); err != nil {
		return a.fail(ExitUsage, err)
	}
	if len(forwarded) == 0 {
		fmt.Fprintf(a.Stdout, "正在使用账号 %s 启动 OpenAI 官方 Codex CLI。后续官方界面（例如目录信任提示）可能显示英文。\n", textsafe.Clean(p.Alias, 63))
	}
	err := a.Runtime.RunInteractive(ctx, p, forwarded, a.Stdin, a.Stdout, a.Stderr)
	if err == nil {
		return ExitOK
	}
	if code, ok := codexruntime.ChildExitCode(err); ok {
		return code
	}
	return a.fail(ExitRuntimeFailure, err)
}

// preflightExecRepository turns Codex's otherwise terse non-interactive exec
// error into an actionable, fail-closed explanation. Interactive `open` is
// deliberately not affected: upstream owns its directory-trust prompt.
func preflightExecRepository(args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("无法确定当前工作目录：%w", err)
	}
	return preflightExecRepositoryAt(args, cwd)
}

func preflightExecRepositoryAt(args []string, cwd string) error {
	workingDir := cwd
	commandSeen := false
	skipValue := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if skipValue {
			skipValue = false
			continue
		}
		switch {
		case arg == "-C" || arg == "--cd":
			if i+1 >= len(args) {
				return nil // Let the official CLI provide its normal syntax error.
			}
			i++
			workingDir = resolveRunDirectory(cwd, args[i])
			continue
		case strings.HasPrefix(arg, "--cd="):
			workingDir = resolveRunDirectory(cwd, strings.TrimPrefix(arg, "--cd="))
			continue
		case strings.HasPrefix(arg, "-C") && len(arg) > 2:
			workingDir = resolveRunDirectory(cwd, arg[2:])
			continue
		}
		if commandSeen {
			continue
		}
		if runOptionConsumesValue(arg) {
			skipValue = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		commandSeen = true
		if arg != "exec" {
			return nil
		}
		if _, err := os.Lstat(filepath.Join(workingDir, ".git")); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("无法检查官方 Codex exec 的 Git 目录：%w", err)
		}
		return fmt.Errorf("官方 Codex exec 需要在 Git 仓库中运行；当前目录 %q 没有 Git metadata。CodexPool 不会自动添加 --skip-git-repo-check。请切换到 Git 仓库，或在 exec 前传入官方 -C <Git仓库路径>", workingDir)
	}
	return nil
}

func resolveRunDirectory(cwd, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(cwd, value)
}

func runOptionConsumesValue(arg string) bool {
	switch arg {
	case "-m", "--model", "-i", "--image", "--add-dir", "-s", "--sandbox", "-a", "--ask-for-approval", "--output-schema", "-o", "--output-last-message", "--color":
		return true
	default:
		return false
	}
}

func (a *App) commandAdd(ctx context.Context, args []string) int {
	alias, storeMode, deviceAuth, yes, err := parseAddArgs(args)
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	automaticAlias := alias == ""
	if automaticAlias {
		alias, err = pendingAlias()
		if err != nil {
			return a.fail(ExitRuntimeFailure, err)
		}
	}
	if !yes {
		label := fmt.Sprintf("账号 %q", alias)
		if automaticAlias {
			label = "验证后的官方账号邮箱（登录成功后自动命名）"
		}
		approved, err := a.confirm(
			fmt.Sprintf("添加%s？将创建新的独立 CODEX_HOME，并启动官方 Codex 登录流程。CodexPool 不会读取或导入现有 ~/.codex 凭据。[y/N] ", label),
		)
		if err != nil {
			return a.fail(ExitUsage, err)
		}
		if !approved {
			fmt.Fprintln(a.Stdout, "已取消，未启动任何登录流程。")
			return ExitOK
		}
	}

	a.syncAuthIO()
	p, result, err := a.Auth.Add(ctx, alias, storeMode, deviceAuth)
	if err != nil {
		if p.ID != "" {
			fmt.Fprintf(a.Stderr, "账号 %s（%s）保留在状态 %s，可稍后显式重试。\n", textsafe.Clean(p.Alias, 63), textsafe.Clean(p.ID, 80), displayState(string(p.Lifecycle)))
		}
		return a.fail(ExitRuntimeFailure, err)
	}
	if automaticAlias {
		p, err = a.renameToVerifiedEmail(p)
		if err != nil {
			return a.fail(ExitRuntimeFailure, fmt.Errorf("官方登录已完成，但无法按验证后的账号邮箱命名；Profile %s 保留为 %q：%w", p.ID, p.Alias, err))
		}
	}
	fmt.Fprintf(a.Stdout, "已添加账号 %s（%s），套餐=%s\n", textsafe.Clean(p.Alias, 63), textsafe.Clean(p.ID, 80), displayPlan(textsafe.CleanOr(p.Identity.PlanType, "—", 80)))
	if result.Cache.Status != "" {
		fmt.Fprintf(a.Stdout, "用量状态：%s\n", displayState(string(result.Cache.Status)))
	}
	return ExitOK
}

func pendingAlias() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成临时 Profile 名称：%w", err)
	}
	return "pending-" + hex.EncodeToString(raw[:]), nil
}

func (a *App) renameToVerifiedEmail(p profile.Profile) (profile.Profile, error) {
	email := strings.TrimSpace(p.Identity.Email)
	if email == "" {
		return p, errors.New("官方 account/read 未提供可验证邮箱")
	}
	if err := profile.ValidateAlias(email); err != nil {
		return p, fmt.Errorf("验证后的邮箱不能作为账号名称：%w", err)
	}
	return a.Store.Rename(p.ID, email)
}

func (a *App) commandRelogin(ctx context.Context, args []string) int {
	alias, deviceAuth, yes, err := parseReloginArgs(args)
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	target, err := a.Store.Resolve(alias)
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	return a.reloginExpected(ctx, target, deviceAuth, yes)
}

func (a *App) reloginExpected(ctx context.Context, target profile.Profile, deviceAuth, yes bool) int {
	if !yes {
		approved, err := a.confirm(
			fmt.Sprintf("重新登录账号 %q（%s，generation=%d）？将使用官方 Codex 登录流程，并使该 Profile 的旧身份缓存 generation 失效。[y/N] ", target.Alias, target.ID, target.AuthGeneration),
		)
		if err != nil {
			return a.fail(ExitUsage, err)
		}
		if !approved {
			fmt.Fprintln(a.Stdout, "已取消，未启动任何登录流程。")
			return ExitOK
		}
	}
	a.syncAuthIO()
	p, result, err := a.Auth.ReloginExpected(ctx, target, deviceAuth)
	if err != nil {
		return a.fail(ExitRuntimeFailure, err)
	}
	fmt.Fprintf(a.Stdout, "账号 %s 已重新认证，generation=%d，用量状态=%s\n", textsafe.Clean(p.Alias, 63), p.AuthGeneration, displayState(string(result.Cache.Status)))
	return ExitOK
}

func (a *App) commandLogout(ctx context.Context, args []string) int {
	alias, yes, err := parseOneAliasWithYes(args, "logout")
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	target, err := a.Store.Resolve(alias)
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	if !yes {
		approved, err := a.confirm(
			fmt.Sprintf("退出账号 %q（%s，generation=%d）？将调用官方 Codex CLI。服务端会话/撤销范围由 Codex 控制，不能保证只影响当前设备。[y/N] ", target.Alias, target.ID, target.AuthGeneration),
		)
		if err != nil {
			return a.fail(ExitUsage, err)
		}
		if !approved {
			fmt.Fprintln(a.Stdout, "已取消。")
			return ExitOK
		}
	}
	a.syncAuthIO()
	p, err := a.Auth.LogoutExpected(ctx, target)
	if err != nil {
		return a.fail(ExitRuntimeFailure, err)
	}
	fmt.Fprintf(a.Stdout, "账号 %s 已退出；本地状态=%s，generation=%d\n", textsafe.Clean(p.Alias, 63), displayState(string(p.Lifecycle)), p.AuthGeneration)
	return ExitOK
}

func (a *App) commandRename(args []string) int {
	if len(args) != 2 {
		return a.fail(ExitUsage, errors.New("用法：./codexpool.sh rename <原名称> <新名称>"))
	}
	p, err := a.Auth.Rename(args[0], args[1])
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	fmt.Fprintf(a.Stdout, "账号名称已修改为 %s；物理 Profile 目录没有移动。\n", textsafe.Clean(p.Alias, 63))
	return ExitOK
}

func (a *App) commandRemove(ctx context.Context, args []string) int {
	alias, purge, yes, err := parseRemoveArgs(args)
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	p, err := a.Store.Resolve(alias)
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	return a.removeExpected(ctx, p, purge, yes)
}

func (a *App) removeExpected(ctx context.Context, p profile.Profile, purge, yes bool) int {
	if purge {
		if !yes {
			approved, err := a.confirm(
				fmt.Sprintf("永久删除账号 %q？CodexPool 会先执行官方 `codex logout`，成功后只删除这个已校验的 Profile 目录。如果退出或本地凭据清理失败，删除会停止，不会假装已完成。[y/N] ", p.Alias),
			)
			if err != nil {
				return a.fail(ExitUsage, err)
			}
			if !approved {
				fmt.Fprintln(a.Stdout, "已取消。")
				return ExitOK
			}
		}
	} else if !yes {
		approved, err := a.confirm(
			fmt.Sprintf("取消注册 %q？本地 Profile 目录和 Codex 管理的凭据会保留，便于恢复。[y/N] ", p.Alias),
		)
		if err != nil {
			return a.fail(ExitUsage, err)
		}
		if !approved {
			fmt.Fprintln(a.Stdout, "已取消。")
			return ExitOK
		}
	}

	a.syncAuthIO()
	removed, err := a.Auth.RemoveExpected(ctx, p, purge)
	if err != nil {
		return a.fail(ExitRuntimeFailure, err)
	}
	if purge {
		fmt.Fprintf(a.Stdout, "账号 %s 已在官方退出后完成永久删除。\n", textsafe.Clean(removed.Alias, 63))
	} else {
		fmt.Fprintf(a.Stdout, "账号 %s 已取消注册；本地数据仍保留在 %s\n", textsafe.Clean(removed.Alias, 63), textsafe.Clean(removed.CodexHome, 300))
	}
	return ExitOK
}

func (a *App) syncAuthIO() {
	if a.Auth == nil {
		return
	}
	a.Auth.Stdin = a.Stdin
	a.Auth.Stdout = a.Stdout
	a.Auth.Stderr = a.Stderr
}

func (a *App) confirm(prompt string) (bool, error) {
	fmt.Fprint(a.Stderr, prompt)
	reader := bufio.NewReader(a.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return false, fmt.Errorf("需要确认；非交互环境请使用 --yes 表示本次明确授权：%w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func splitDoubleDash(args []string) ([]string, []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func parseAddArgs(args []string) (string, profile.CredentialStore, bool, bool, error) {
	storeMode := profile.StoreFile
	var alias string
	var deviceAuth, yes bool
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--device-auth":
			deviceAuth = true
		case arg == "--yes":
			yes = true
		case arg == "--store":
			i++
			if i >= len(args) {
				return "", "", false, false, errors.New("--store 需要指定 file；v0.1 preview 暂未启用钥匙串存储")
			}
			storeMode = profile.CredentialStore(args[i])
		case strings.HasPrefix(arg, "--store="):
			storeMode = profile.CredentialStore(strings.TrimPrefix(arg, "--store="))
		case strings.HasPrefix(arg, "-"):
			return "", "", false, false, fmt.Errorf("未知的 add 选项 %q", arg)
		default:
			if alias != "" {
				return "", "", false, false, errors.New("add 只能指定一个账号名称")
			}
			alias = arg
		}
	}
	if storeMode == profile.StoreKeyring {
		return "", "", false, false, errors.New("v0.1 preview 暂未启用钥匙串凭据存储；需完成平台级隔离与清理验证后再开放")
	}
	if storeMode != profile.StoreFile {
		return "", "", false, false, fmt.Errorf("不支持的凭据存储方式 %q", storeMode)
	}
	return alias, storeMode, deviceAuth, yes, nil
}

func parseReloginArgs(args []string) (string, bool, bool, error) {
	var alias string
	var deviceAuth, yes bool
	for _, arg := range args {
		switch arg {
		case "--device-auth":
			deviceAuth = true
		case "--yes":
			yes = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, false, fmt.Errorf("未知的 relogin 选项 %q", arg)
			}
			if alias != "" {
				return "", false, false, errors.New("relogin 只能指定一个账号名称")
			}
			alias = arg
		}
	}
	if alias == "" {
		return "", false, false, errors.New("用法：./codexpool.sh relogin <账号名称> [--device-auth] [--yes]")
	}
	return alias, deviceAuth, yes, nil
}

func parseOneAliasWithYes(args []string, command string) (string, bool, error) {
	var alias string
	var yes bool
	for _, arg := range args {
		switch arg {
		case "--yes":
			yes = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, fmt.Errorf("未知的 %s 选项 %q", command, arg)
			}
			if alias != "" {
				return "", false, fmt.Errorf("%s 只能指定一个账号名称", command)
			}
			alias = arg
		}
	}
	if alias == "" {
		return "", false, fmt.Errorf("用法：./codexpool.sh %s <账号名称> [--yes]", command)
	}
	return alias, yes, nil
}

func parseRemoveArgs(args []string) (string, bool, bool, error) {
	var alias string
	var purge, yes bool
	for _, arg := range args {
		switch arg {
		case "--purge":
			purge = true
		case "--yes":
			yes = true
		default:
			if strings.HasPrefix(arg, "-") {
				return "", false, false, fmt.Errorf("未知的 remove 选项 %q", arg)
			}
			if alias != "" {
				return "", false, false, errors.New("remove 只能指定一个账号名称")
			}
			alias = arg
		}
	}
	if alias == "" {
		return "", false, false, errors.New("用法：./codexpool.sh remove <账号名称> [--purge] [--yes]")
	}
	return alias, purge, yes, nil
}
