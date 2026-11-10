package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"codexpool/internal/desktopactivation"
	"codexpool/internal/diagnostics"
	"codexpool/internal/profile"
	"codexpool/internal/textsafe"
	"codexpool/internal/tui"
)

func (a *App) commandDoctor(ctx context.Context, args []string) int {
	var jsonMode, probeRuntime bool
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonMode = true
		case "--probe-runtime":
			probeRuntime = true
		default:
			return a.fail(ExitUsage, fmt.Errorf("未知的 doctor 选项 %q", arg))
		}
	}
	report := diagnostics.Run(ctx, a.Store, a.Runtime, a.EvidenceManifest, probeRuntime)
	exitCode := doctorExitCode(report)
	if jsonMode {
		if writeCode := a.writeJSON(report); writeCode != ExitOK {
			return writeCode
		}
		return exitCode
	}
	fmt.Fprintf(a.Stdout, "平台：%s/%s\n", textsafe.Clean(report.Platform, 40), textsafe.Clean(report.Architecture, 40))
	fmt.Fprintf(a.Stdout, "CodexPool 根目录：%s\n", textsafe.Clean(report.CodexPoolRoot, 300))
	fmt.Fprintf(a.Stdout, "Codex 可执行文件：%s\n", textsafe.Clean(report.CodexBinary, 300))
	if report.RuntimeVersion != "" {
		fmt.Fprintf(a.Stdout, "运行时版本：%s\n", textsafe.Clean(report.RuntimeVersion, 80))
	}
	if report.Evidence != nil {
		fmt.Fprintf(a.Stdout, "协议证据：Codex %s（%s），源码提交 %s\n",
			textsafe.Clean(report.Evidence.CodexVersion, 80), textsafe.Clean(report.Evidence.ResearchDate, 40), textsafe.Clean(report.Evidence.SourceCommitReviewed, 100))
	}
	for _, check := range report.Checks {
		fmt.Fprintf(a.Stdout, "[%-4s] %-28s %s\n", textsafe.Clean(strings.ToUpper(check.Status), 12), textsafe.Clean(check.Name, 80), textsafe.Clean(check.Detail, 500))
	}
	return exitCode
}

func doctorExitCode(report diagnostics.Report) int {
	for _, check := range report.Checks {
		if check.Status == "fail" {
			return ExitUnsupported
		}
	}
	return ExitOK
}

func (a *App) commandTUI(ctx context.Context, args []string) int {
	if len(args) != 0 {
		return a.fail(ExitUsage, errors.New("用法：./codexpool.sh tui"))
	}
	if !writerIsTTY(a.Stdout) {
		return a.fail(ExitUsage, errors.New("终端面板需要在交互式终端中运行"))
	}
	initial, err := a.buildTUIRows(ctx, true)
	if err != nil {
		return a.fail(ExitRuntimeFailure, err)
	}
	desktop := a.desktopTUIState()
	action, err := tui.RunWithDesktopState(ctx, initial, func(refreshCtx context.Context) ([]tui.Row, error) {
		return a.buildTUIRows(refreshCtx, false)
	}, desktop)
	if err != nil {
		return a.fail(ExitRuntimeFailure, err)
	}
	switch action.Kind {
	case tui.ActionNone:
		return ExitOK
	case tui.ActionRun:
		p, err := a.resolveTUITarget(action.Target)
		if err != nil {
			return a.fail(ExitRuntimeFailure, err)
		}
		return a.runExpected(ctx, p, nil)
	case tui.ActionUse:
		p, err := a.resolveTUITarget(action.Target)
		if err != nil {
			return a.fail(ExitRuntimeFailure, err)
		}
		return a.useExpected(p)
	case tui.ActionAdd:
		return a.commandAdd(ctx, nil)
	case tui.ActionRelogin:
		p, err := a.resolveTUITarget(action.Target)
		if err != nil {
			return a.fail(ExitRuntimeFailure, err)
		}
		return a.reloginExpected(ctx, p, false, false)
	case tui.ActionRemove:
		p, err := a.resolveTUITarget(action.Target)
		if err != nil {
			return a.fail(ExitRuntimeFailure, err)
		}
		return a.removeExpected(ctx, p, false, false)
	case tui.ActionDesktopActivate:
		p, err := a.resolveTUITarget(action.Target)
		if err != nil {
			return a.fail(ExitRuntimeFailure, err)
		}
		fmt.Fprintln(a.Stdout, "TUI 没有修改 Desktop。若你要继续，请先阅读提示并由你本人执行以下单独 helper：")
		fmt.Fprintf(a.Stdout, "go run ./cmd/codexpool-desktop-helper activate --profile-id %s --i-understand-this-switches-desktop-auth --timeout 15m\n", p.ID)
		return ExitOK
	case tui.ActionDesktopRestore:
		fmt.Fprintln(a.Stdout, "TUI 没有修改 Desktop。若要恢复此前 Desktop 登录，请由你本人执行：")
		fmt.Fprintln(a.Stdout, "go run ./cmd/codexpool-desktop-helper restore --i-understand-this-switches-desktop-auth --timeout 15m")
		return ExitOK
	default:
		return a.fail(ExitRuntimeFailure, fmt.Errorf("未知的终端面板操作 %q", action.Kind))
	}
}

func (a *App) desktopTUIState() tui.DesktopState {
	state, err := desktopactivation.InspectLiveState(a.Store)
	if err != nil {
		return tui.DesktopState{Label: "状态异常", Warning: "无法读取 CodexPool 的 Desktop 恢复记录；不要切换 Desktop，请先使用独立 helper 排查。"}
	}
	if state.Kind == desktopactivation.LiveStateNone {
		return tui.DesktopState{Label: "无临时切换记录"}
	}
	label := "未知账号"
	if p, resolveErr := a.Store.Resolve(state.Target.ProfileID); resolveErr == nil {
		label = textsafe.CleanOr(p.Identity.Email, p.Alias, 32)
	}
	if state.Kind == desktopactivation.LiveStateRecoveryRequired {
		return tui.DesktopState{Label: "恢复待处理", Warning: "Desktop 切换曾中断或尚未完成恢复；请勿再次激活，先按 V 查看恢复指令。"}
	}
	return tui.DesktopState{Label: "临时账号 " + label, Warning: "CodexPool 记录显示 Desktop 仍可能处于临时账号状态；完成后请按 V 查看恢复指令。"}
}

func (a *App) resolveTUITarget(target tui.ActionTarget) (profile.Profile, error) {
	if target.ProfileID == "" {
		return profile.Profile{}, errors.New("终端面板操作缺少不可变账号 ID")
	}
	p, err := a.Store.Resolve(target.ProfileID)
	if err != nil {
		return profile.Profile{}, err
	}
	if p.AuthGeneration != target.AuthGeneration || string(p.Lifecycle) != target.Lifecycle {
		return p, fmt.Errorf("%w：该账号在界面显示后状态已经变化，请返回账号池刷新后重试", profile.ErrProfileChanged)
	}
	return p, nil
}

func (a *App) promptAlias(prompt string) (string, error) {
	fmt.Fprint(a.Stderr, prompt)
	reader := bufio.NewReader(a.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	alias := strings.TrimSpace(line)
	return alias, nil
}
