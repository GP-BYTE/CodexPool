package cli

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"codexpool/internal/desktopactivation"
	"codexpool/internal/profile"
	"codexpool/internal/textsafe"
)

//go:embed webassets/index.html webassets/app.css webassets/dashboard.css webassets/app.js
var webAssets embed.FS

type webConsole struct {
	app            *App
	authority      string
	token          string
	repoRoot       string
	launchTerminal func(context.Context, string) error
	refreshMu      sync.Mutex
}

type webAccount struct {
	Alias          string             `json:"alias"`
	Email          string             `json:"email"`
	ProfileID      string             `json:"profileId"`
	AuthGeneration uint64             `json:"authGeneration"`
	Lifecycle      profile.Lifecycle  `json:"lifecycle"`
	Active         bool               `json:"active"`
	Plan           string             `json:"plan"`
	State          string             `json:"state"`
	UsageStatus    string             `json:"usageStatus"`
	FetchedAt      time.Time          `json:"fetchedAt,omitempty"`
	FiveHLeft      *float64           `json:"fiveHourLeft"`
	WeeklyLeft     *float64           `json:"weeklyLeft"`
	OtherWindows   []statusWindowView `json:"otherWindows"`
	Credits        string             `json:"credits"`
	ResetCredit    string             `json:"resetCredits"`
}

type webState struct {
	Accounts []webAccount `json:"accounts"`
	Desktop  struct {
		Kind  desktopactivation.LiveState `json:"kind"`
		Label string                      `json:"label"`
	} `json:"desktop"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type webTarget struct {
	ProfileID      string            `json:"profileId"`
	AuthGeneration uint64            `json:"authGeneration"`
	Lifecycle      profile.Lifecycle `json:"lifecycle"`
}

type webAction struct {
	Action   string    `json:"action"`
	Target   webTarget `json:"target"`
	NewAlias string    `json:"newAlias"`
}

func (a *App) commandWeb(ctx context.Context, args []string) int {
	noOpen := false
	for _, arg := range args {
		if arg != "--no-open" || noOpen {
			return a.fail(ExitUsage, fmt.Errorf("用法：./codexpool-web.sh [--no-open]"))
		}
		noOpen = true
	}
	if runtime.GOOS != "darwin" && !noOpen {
		return a.fail(ExitUnsupported, errors.New("自动打开浏览器目前仅支持 macOS；可使用 --no-open 后自行打开显示的本机地址"))
	}
	repoRoot, err := os.Getwd()
	if err != nil {
		return a.fail(ExitRuntimeFailure, err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "codexpool.sh")); err != nil {
		return a.fail(ExitUsage, errors.New("请从 CodexPool 项目目录启动网页控制台"))
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return a.fail(ExitRuntimeFailure, fmt.Errorf("启动本机网页服务：%w", err))
	}
	defer listener.Close()
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return a.fail(ExitRuntimeFailure, fmt.Errorf("生成网页会话密钥：%w", err))
	}
	console := &webConsole{
		app:            a,
		authority:      listener.Addr().String(),
		token:          hex.EncodeToString(tokenBytes),
		repoRoot:       repoRoot,
		launchTerminal: openTerminalCommand,
	}
	server := &http.Server{
		Handler:           console.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	url := "http://" + console.authority + "/"
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Fprintf(a.Stdout, "网页控制台已启动：%s\n仅此电脑可访问；关闭终端或按 Ctrl+C 即停止。\n", url)
	if !noOpen {
		openCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		if err := exec.CommandContext(openCtx, "open", url).Run(); err != nil {
			fmt.Fprintf(a.Stderr, "未能自动打开浏览器；请手动打开上面的本机地址：%s\n", textsafe.Clean(err.Error(), 300))
		}
		cancel()
	}
	if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return a.fail(ExitRuntimeFailure, err)
	}
	return ExitOK
}

func (w *webConsole) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", w.serveIndex)
	mux.HandleFunc("GET /app.css", w.serveAsset("webassets/app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /dashboard.css", w.serveAsset("webassets/dashboard.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /app.js", w.serveAsset("webassets/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /api/state", w.serveState)
	mux.HandleFunc("POST /api/refresh", w.serveRefresh)
	mux.HandleFunc("POST /api/use", w.serveUse)
	mux.HandleFunc("POST /api/terminal", w.serveTerminal)
	return http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		out.Header().Set("Cache-Control", "no-store")
		out.Header().Set("X-Content-Type-Options", "nosniff")
		out.Header().Set("X-Frame-Options", "DENY")
		out.Header().Set("Referrer-Policy", "no-referrer")
		out.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		if r.Host != w.authority {
			http.Error(out, "本机地址不匹配", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && (r.Header.Get("Origin") != "http://"+w.authority || r.Header.Get("X-CodexPool-Session") != w.token) {
			http.Error(out, "网页会话无效，请重新打开控制台", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(out, r)
	})
}

func (w *webConsole) serveIndex(out http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(out, r)
		return
	}
	data, err := webAssets.ReadFile("webassets/index.html")
	if err != nil {
		http.Error(out, "页面不可用", http.StatusInternalServerError)
		return
	}
	page, err := template.New("index").Parse(string(data))
	if err != nil {
		http.Error(out, "页面不可用", http.StatusInternalServerError)
		return
	}
	out.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = page.Execute(out, struct{ Token string }{w.token})
}

func (w *webConsole) serveAsset(name, contentType string) http.HandlerFunc {
	return func(out http.ResponseWriter, _ *http.Request) {
		data, err := webAssets.ReadFile(name)
		if err != nil {
			http.Error(out, "资源不可用", http.StatusInternalServerError)
			return
		}
		out.Header().Set("Content-Type", contentType)
		_, _ = out.Write(data)
	}
}

func (w *webConsole) serveState(out http.ResponseWriter, r *http.Request) {
	state, err := w.readState(r.Context(), false)
	if err != nil {
		webError(out, http.StatusInternalServerError, err)
		return
	}
	webJSON(out, http.StatusOK, state)
}

func (w *webConsole) serveRefresh(out http.ResponseWriter, r *http.Request) {
	if !w.refreshMu.TryLock() {
		webError(out, http.StatusConflict, errors.New("正在刷新，请稍候"))
		return
	}
	defer w.refreshMu.Unlock()
	state, err := w.readState(r.Context(), true)
	if err != nil {
		webError(out, http.StatusInternalServerError, err)
		return
	}
	webJSON(out, http.StatusOK, state)
}

func (w *webConsole) readState(ctx context.Context, live bool) (webState, error) {
	profiles, activeID, err := w.app.Store.List()
	if err != nil {
		return webState{}, err
	}
	var views []statusView
	if live {
		views = w.app.collectStatus(ctx, profiles, activeID, false)
	} else {
		views = w.app.collectStatus(ctx, profiles, activeID, true)
	}
	state := webState{Accounts: make([]webAccount, 0, len(profiles)), UpdatedAt: time.Now().UTC()}
	for i, p := range profiles {
		v := views[i]
		state.Accounts = append(state.Accounts, webAccount{
			Alias:          textsafe.Clean(p.Alias, 80),
			Email:          textsafe.Clean(p.Identity.Email, 120),
			ProfileID:      p.ID,
			AuthGeneration: p.AuthGeneration,
			Lifecycle:      p.Lifecycle,
			Active:         p.ID == activeID,
			Plan:           displayPlan(v.Plan),
			State:          displayState(statusState(v)),
			UsageStatus:    string(v.UsageStatus),
			FetchedAt:      v.FetchedAt,
			FiveHLeft:      v.FiveHLeft,
			WeeklyLeft:     v.WeeklyLeft,
			OtherWindows:   v.OtherWindows,
			Credits:        textsafe.CleanOr(v.Credits, "—", 60),
			ResetCredit:    textsafe.CleanOr(v.ResetCredit, "—", 40),
		})
	}
	desktop, err := desktopactivation.InspectLiveState(w.app.Store)
	if err != nil {
		state.Desktop.Kind = desktopactivation.LiveStateNone
		state.Desktop.Label = "切换记录状态异常；请在终端运行 Desktop helper status 排查"
		return state, nil
	}
	state.Desktop.Kind = desktop.Kind
	switch desktop.Kind {
	case desktopactivation.LiveStateActive:
		state.Desktop.Label = "存在临时切换记录"
	case desktopactivation.LiveStateRecoveryRequired:
		state.Desktop.Label = "需要恢复"
	default:
		state.Desktop.Label = "无临时切换记录"
	}
	return state, nil
}

func (w *webConsole) serveUse(out http.ResponseWriter, r *http.Request) {
	var input webAction
	if !decodeWebAction(out, r, &input) {
		return
	}
	if input.Action != "use" {
		webError(out, http.StatusBadRequest, errors.New("未知操作"))
		return
	}
	p, err := w.resolveTarget(input.Target)
	if err != nil {
		webError(out, http.StatusConflict, err)
		return
	}
	if p.Lifecycle != profile.LifecycleReady {
		webError(out, http.StatusConflict, errors.New("该账号尚未就绪"))
		return
	}
	if _, err := w.app.Store.SetActiveExpected(p); err != nil {
		webError(out, http.StatusConflict, err)
		return
	}
	state, err := w.readState(r.Context(), false)
	if err != nil {
		webError(out, http.StatusInternalServerError, err)
		return
	}
	webJSON(out, http.StatusOK, state)
}

func (w *webConsole) resolveTarget(target webTarget) (profile.Profile, error) {
	if target.ProfileID == "" || target.AuthGeneration == 0 || target.Lifecycle == "" {
		return profile.Profile{}, errors.New("账号目标不完整，请刷新页面后重试")
	}
	p, err := w.app.Store.Resolve(target.ProfileID)
	if err != nil {
		return profile.Profile{}, err
	}
	if p.ID != target.ProfileID || p.AuthGeneration != target.AuthGeneration || p.Lifecycle != target.Lifecycle {
		return profile.Profile{}, fmt.Errorf("%w：账号状态已变化，请刷新页面后重试", profile.ErrProfileChanged)
	}
	return p, nil
}

func (w *webConsole) serveTerminal(out http.ResponseWriter, r *http.Request) {
	var input webAction
	if !decodeWebAction(out, r, &input) {
		return
	}
	command, err := w.terminalCommand(input)
	if err != nil {
		webError(out, http.StatusConflict, err)
		return
	}
	if runtime.GOOS != "darwin" {
		webJSON(out, http.StatusOK, map[string]string{"message": "请在终端中运行以下命令", "command": command})
		return
	}
	launchCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := w.launchTerminal(launchCtx, command); err != nil {
		webJSON(out, http.StatusOK, map[string]string{"message": "无法自动打开终端，请复制命令手动运行", "command": command})
		return
	}
	webJSON(out, http.StatusOK, map[string]string{"message": "终端已打开，请在那里完成官方流程"})
}

func (w *webConsole) terminalCommand(input webAction) (string, error) {
	base := "cd " + shellQuote(w.repoRoot) + " && "
	switch input.Action {
	case "add":
		return base + "./codexpool.sh add", nil
	case "doctor":
		return base + "./codexpool.sh doctor", nil
	case "desktopRestore", "desktopRestoreNoLaunch":
		state, err := desktopactivation.InspectLiveState(w.app.Store)
		if err != nil {
			return "", err
		}
		if state.Kind == desktopactivation.LiveStateNone {
			return "", errors.New("当前没有待恢复的 Desktop 切换记录")
		}
		command := base + "go run ./cmd/codexpool-desktop-helper restore --i-understand-this-switches-desktop-auth --timeout 15m"
		if input.Action == "desktopRestoreNoLaunch" {
			command += " --no-launch"
		}
		return command, nil
	}
	p, err := w.resolveTarget(input.Target)
	if err != nil {
		return "", err
	}
	id := shellQuote(p.ID)
	switch input.Action {
	case "run":
		if p.Lifecycle != profile.LifecycleReady {
			return "", errors.New("该账号尚未就绪")
		}
		return base + "./codexpool.sh open " + id, nil
	case "relogin":
		return base + "./codexpool.sh relogin " + id, nil
	case "logout":
		return base + "./codexpool.sh logout " + id, nil
	case "unregister":
		return base + "./codexpool.sh remove " + id, nil
	case "delete":
		return base + "./codexpool.sh delete " + id, nil
	case "rename":
		if err := profile.ValidateAlias(input.NewAlias); err != nil {
			return "", err
		}
		return base + "./codexpool.sh rename " + id + " " + shellQuote(input.NewAlias), nil
	case "desktopActivate":
		if p.Lifecycle != profile.LifecycleReady {
			return "", errors.New("该账号尚未就绪")
		}
		state, err := desktopactivation.InspectLiveState(w.app.Store)
		if err != nil {
			return "", err
		}
		if state.Kind == desktopactivation.LiveStateRecoveryRequired {
			return "", errors.New("Desktop 切换待恢复，请先恢复原始登录")
		}
		if state.Kind == desktopactivation.LiveStateActive && state.Target.ProfileID == p.ID && state.Target.AuthGeneration == p.AuthGeneration && state.Target.Lifecycle == p.Lifecycle {
			return "", errors.New("该账号已经有 Desktop 临时切换记录；完成后请恢复原始登录")
		}
		return base + "go run ./cmd/codexpool-desktop-helper activate --profile-id " + id + " --i-understand-this-switches-desktop-auth --timeout 15m", nil
	default:
		return "", errors.New("未知操作")
	}
}

func openTerminalCommand(ctx context.Context, command string) error {
	return exec.CommandContext(ctx, "osascript",
		"-e", "on run argv",
		"-e", "tell application \"Terminal\"",
		"-e", "do script (item 1 of argv)",
		"-e", "activate",
		"-e", "end tell",
		"-e", "end run",
		command,
	).Run()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func decodeWebAction(out http.ResponseWriter, r *http.Request, value *webAction) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		webError(out, http.StatusUnsupportedMediaType, errors.New("需要 JSON 请求"))
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(out, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		webError(out, http.StatusBadRequest, errors.New("操作参数无效"))
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		webError(out, http.StatusBadRequest, errors.New("操作参数无效"))
		return false
	}
	return true
}

func webJSON(out http.ResponseWriter, status int, value any) {
	out.Header().Set("Content-Type", "application/json; charset=utf-8")
	out.WriteHeader(status)
	_ = json.NewEncoder(out).Encode(value)
}

func webError(out http.ResponseWriter, status int, err error) {
	webJSON(out, status, map[string]string{"error": textsafe.Clean(err.Error(), 240)})
}
