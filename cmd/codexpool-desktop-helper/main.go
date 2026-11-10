// codexpool-desktop-helper is intentionally separate from the everyday CLI.
// It is a two-phase, explicitly-confirmed macOS integration test helper.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"codexpool/internal/desktopactivation"
	"codexpool/internal/profile"
	"codexpool/internal/securefs"
)

type launcher struct{}

func (launcher) Verify(ctx context.Context, _ desktopactivation.Target) error {
	return desktopactivation.OpenChatGPT(ctx, nil)
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "此 Desktop 集成测试 helper 仅支持 macOS。")
		return 4
	}
	if len(args) == 0 || (args[0] != "activate" && args[0] != "restore" && args[0] != "status") {
		fmt.Fprintln(os.Stderr, "用法：codexpool-desktop-helper status；activate --profile-id <p_...> --i-understand-this-switches-desktop-auth；或 restore --i-understand-this-switches-desktop-auth [--no-launch]")
		return 2
	}
	mode := args[0]
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	profileID := fs.String("profile-id", "", "immutable CodexPool Profile ID")
	confirmed := fs.Bool("i-understand-this-switches-desktop-auth", false, "required explicit acknowledgement")
	noLaunch := fs.Bool("no-launch", false, "restore authentication state without opening ChatGPT")
	timeout := fs.Duration("timeout", 15*time.Minute, "maximum wait for a full Desktop exit")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if mode != "status" && !*confirmed {
		fmt.Fprintln(os.Stderr, "拒绝执行：必须提供 --i-understand-this-switches-desktop-auth。")
		return 2
	}
	if mode != "status" && *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "拒绝执行：--timeout 必须为正数。")
		return 2
	}
	root, err := securefs.DefaultRoot()
	if err != nil {
		return fail(err)
	}
	store, err := profile.NewStore(root)
	if err != nil {
		return fail(err)
	}
	if mode == "status" {
		if *profileID != "" || *confirmed || *noLaunch || fs.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "status 不接受其它参数。")
			return 2
		}
		return printStatus(store)
	}
	state, err := desktopactivation.InspectLiveState(store)
	if err != nil {
		return fail(err)
	}
	if mode == "activate" && state.Kind == desktopactivation.LiveStateRecoveryRequired {
		fmt.Fprintln(os.Stderr, "检测到未完成的 Desktop 切换；本次没有开始新的切换。请先运行 restore 恢复第一次切换前的 Desktop 登录状态，再决定是否重新 activate。")
		return 6
	}
	if mode == "restore" && state.Kind == desktopactivation.LiveStateNone {
		if *profileID != "" || fs.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "restore 不接受 --profile-id 或额外参数。")
			return 2
		}
		fmt.Fprintln(os.Stdout, "没有待恢复的 Desktop 切换记录；未修改 Desktop。")
		return 0
	}
	engine, err := desktopactivation.NewLive(store)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	inspector := desktopactivation.MacDesktopInspector{}
	fmt.Fprintln(os.Stdout, "请现在使用 Cmd+Q 完整退出 ChatGPT Desktop；helper 只会等待，不会主动杀进程。")
	if err := desktopactivation.WaitForDesktopDrain(ctx, inspector, time.Second); err != nil {
		return fail(err)
	}
	if mode == "restore" {
		if *profileID != "" || fs.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "restore 不接受 --profile-id 或额外参数。")
			return 2
		}
		if err := engine.RestoreOriginal(); err != nil {
			return fail(err)
		}
		if *noLaunch {
			fmt.Fprintln(os.Stdout, "已恢复第一次切换前的 Desktop 本地认证状态；按你的要求没有启动 ChatGPT，可由你手动打开确认。")
			return 0
		}
		if err := desktopactivation.OpenChatGPT(ctx, nil); err != nil {
			fmt.Fprintln(os.Stderr, "Desktop 本地认证状态已恢复，但未能自动启动 ChatGPT；请手动打开 ChatGPT 确认。", err)
			return 5
		}
		fmt.Fprintln(os.Stdout, "已恢复第一次切换前的 Desktop 本地认证状态并请求重新启动 ChatGPT。")
		return 0
	}
	if *profileID == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "activate 必须且只能提供 --profile-id <p_...>。")
		return 2
	}
	p, err := store.Resolve(*profileID)
	if err != nil {
		return fail(err)
	}
	target := desktopactivation.Target{ProfileID: p.ID, AuthGeneration: p.AuthGeneration, Lifecycle: p.Lifecycle}
	if err := engine.Activate(ctx, target, inspector, launcher{}); err != nil {
		return fail(err)
	}
	fmt.Fprintln(os.Stdout, "已请求启动 ChatGPT。请由用户本人核对 Desktop 中显示的小号身份；完成后运行 restore 恢复原账号。")
	return 0
}

func printStatus(store *profile.Store) int {
	state, err := desktopactivation.InspectLiveState(store)
	if err != nil {
		return fail(err)
	}
	switch state.Kind {
	case desktopactivation.LiveStateNone:
		fmt.Fprintln(os.Stdout, "Desktop：没有 CodexPool 切换记录；未修改 Desktop。")
	case desktopactivation.LiveStateActive:
		fmt.Fprintln(os.Stdout, "Desktop：存在已完成的 CodexPool 临时切换记录。运行 restore 可恢复第一次切换前的 Desktop 登录状态。")
	case desktopactivation.LiveStateRecoveryRequired:
		fmt.Fprintln(os.Stdout, "Desktop：检测到未完成的切换。不要再次 activate；请先运行 restore（必要时加 --no-launch）恢复第一次切换前的 Desktop 登录状态。")
	default:
		return fail(errors.New("unknown Desktop activation state"))
	}
	return 0
}

func fail(err error) int {
	if errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintln(os.Stderr, "等待 Desktop 完整退出超时；没有进行新的切换。")
		return 6
	}
	fmt.Fprintln(os.Stderr, "desktop helper:", err)
	return 5
}
