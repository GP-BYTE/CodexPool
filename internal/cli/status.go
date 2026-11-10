package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"codexpool/internal/codexruntime"
	"codexpool/internal/profile"
	"codexpool/internal/scheduler"
	"codexpool/internal/selector"
	"codexpool/internal/textsafe"
	"codexpool/internal/tui"
	"codexpool/internal/usage"
)

const selectorCacheMaxAge = 2 * time.Minute

type statusWindowView struct {
	Label        string  `json:"label"`
	DurationMins *int64  `json:"durationMins,omitempty"`
	Remaining    float64 `json:"remainingPercent"`
	SourceKey    string  `json:"sourceKey,omitempty"`
}

type statusView struct {
	Alias          string             `json:"alias"`
	ProfileID      string             `json:"profileId"`
	AuthGeneration uint64             `json:"authGeneration"`
	Active         bool               `json:"active"`
	Lifecycle      profile.Lifecycle  `json:"lifecycle"`
	Plan           string             `json:"plan,omitempty"`
	Store          string             `json:"credentialStore"`
	UsageStatus    usage.CacheStatus  `json:"usageStatus,omitempty"`
	FetchedAt      time.Time          `json:"fetchedAt,omitempty"`
	ErrorCode      string             `json:"errorCode,omitempty"`
	FiveHLeft      *float64           `json:"fiveHourLeft,omitempty"`
	WeeklyLeft     *float64           `json:"weeklyLeft,omitempty"`
	OtherWindows   []statusWindowView `json:"otherWindows,omitempty"`
	Credits        string             `json:"credits,omitempty"`
	ResetCredit    string             `json:"resetCredits,omitempty"`
	BucketKey      string             `json:"summaryBucket,omitempty"`
	Warnings       []string           `json:"warnings,omitempty"`
	Cache          *usage.CacheRecord `json:"cache,omitempty"`
}

func (a *App) commandList(args []string) int {
	jsonMode := false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonMode = true
		default:
			return a.fail(ExitUsage, fmt.Errorf("未知的 list 选项 %q", arg))
		}
	}
	profiles, activeID, err := a.Store.List()
	if err != nil {
		return a.fail(ExitRuntimeFailure, err)
	}
	if jsonMode {
		payload := struct {
			ActiveProfileID string            `json:"activeProfileId,omitempty"`
			Profiles        []profile.Profile `json:"profiles"`
		}{ActiveProfileID: activeID, Profiles: profiles}
		return a.writeJSON(payload)
	}
	if len(profiles) == 0 {
		fmt.Fprintln(a.Stdout, "还没有 CodexPool 账号。可执行：./codexpool.sh add")
		return ExitOK
	}
	fmt.Fprintln(a.Stdout, "账号名称              默认  状态              套餐        凭据存储")
	for _, p := range profiles {
		active := ""
		if p.ID == activeID {
			active = "●"
		}
		plan := p.Identity.PlanType
		if plan == "" {
			plan = "—"
		}
		fmt.Fprintf(a.Stdout, "%-20s %-4s %-16s %-11s %s\n",
			textsafe.Clean(p.Alias, 20), active, displayState(string(p.Lifecycle)), displayPlan(plan), displayStore(string(p.CredentialStore)))
	}
	return ExitOK
}

func (a *App) commandStatus(ctx context.Context, args []string) int {
	opts, err := parseStatusArgs(args)
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	profiles, activeID, err := a.statusTargets(opts.alias, opts.all)
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	views := a.collectStatus(ctx, profiles, activeID, opts.cached)
	exitCode := statusExitCode(views, opts.all)
	if opts.jsonMode {
		if writeCode := a.writeJSON(views); writeCode != ExitOK {
			return writeCode
		}
		return exitCode
	}
	printStatusTable(a.Stdout, views)
	return exitCode
}

type statusOptions struct {
	alias    string
	all      bool
	jsonMode bool
	cached   bool
}

func parseStatusArgs(args []string) (statusOptions, error) {
	var opts statusOptions
	for _, arg := range args {
		switch arg {
		case "--all":
			opts.all = true
		case "--json":
			opts.jsonMode = true
		case "--cached":
			opts.cached = true
		default:
			if strings.HasPrefix(arg, "-") {
				return statusOptions{}, fmt.Errorf("未知的 status 选项 %q", arg)
			}
			if opts.alias != "" {
				return statusOptions{}, errors.New("status 最多只能指定一个账号名称")
			}
			opts.alias = arg
		}
	}
	if opts.all && opts.alias != "" {
		return statusOptions{}, errors.New("status 不能同时指定账号名称和 --all")
	}
	return opts, nil
}

func (a *App) statusTargets(alias string, all bool) ([]profile.Profile, string, error) {
	profiles, activeID, err := a.Store.List()
	if err != nil {
		return nil, "", err
	}
	if all {
		return profiles, activeID, nil
	}
	if alias != "" {
		p, err := a.Store.Resolve(alias)
		if err != nil {
			return nil, "", err
		}
		return []profile.Profile{p}, activeID, nil
	}
	p, err := a.Store.Active()
	if err != nil {
		return nil, "", fmt.Errorf("尚未设置默认账号；请指定账号名称或使用 --all：%w", err)
	}
	return []profile.Profile{p}, activeID, nil
}

func (a *App) collectStatus(ctx context.Context, profiles []profile.Profile, activeID string, cachedOnly bool) []statusView {
	views := make([]statusView, len(profiles))
	if cachedOnly {
		for i, p := range profiles {
			views[i] = a.statusOneCached(p, activeID)
		}
		return views
	}

	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for i, p := range profiles {
		i, p := i, p
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.Lifecycle != profile.LifecycleReady {
				views[i] = a.statusOneCached(p, activeID)
				return
			}
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				views[i] = a.statusOneCached(p, activeID)
				views[i].ErrorCode = "canceled"
				return
			}
			result, err := a.Usage.Fetch(ctx, p)
			current := p
			if err != nil {
				if resolved, resolveErr := a.Store.Resolve(p.ID); resolveErr == nil {
					current = resolved
				}
			}
			views[i] = viewFromCache(current, activeID, result.Cache)
			if err != nil {
				views[i].ErrorCode = classifyStatusError(err)
			}
		}()
	}
	wg.Wait()
	return views
}

func (a *App) statusOneCached(p profile.Profile, activeID string) statusView {
	record, err := a.Usage.Cached(p)
	if err != nil {
		errorCode := "noCache"
		if p.Lifecycle == profile.LifecycleReauthRequired {
			errorCode = "notAuthenticated"
		}
		return statusView{
			Alias:          p.Alias,
			ProfileID:      p.ID,
			AuthGeneration: p.AuthGeneration,
			Active:         p.ID == activeID,
			Lifecycle:      p.Lifecycle,
			Plan:           p.Identity.PlanType,
			Store:          string(p.CredentialStore),
			ErrorCode:      errorCode,
			Credits:        "—",
			ResetCredit:    "—",
		}
	}
	if record.Status == usage.CacheFresh && !usage.IsFresh(record, selectorCacheMaxAge, time.Now()) {
		record.Status = usage.CacheStale
	}
	return viewFromCache(p, activeID, record)
}

func viewFromCache(p profile.Profile, activeID string, record usage.CacheRecord) statusView {
	view := statusView{
		Alias:          p.Alias,
		ProfileID:      p.ID,
		AuthGeneration: p.AuthGeneration,
		Active:         p.ID == activeID,
		Lifecycle:      p.Lifecycle,
		Plan:           p.Identity.PlanType,
		Store:          string(p.CredentialStore),
		UsageStatus:    record.Status,
		FetchedAt:      record.FetchedAt,
		ErrorCode:      record.ErrorCode,
		Credits:        "—",
		ResetCredit:    usage.ResetCreditText(record),
		Cache:          &record,
	}
	if record.Snapshot == nil {
		return view
	}
	view.Warnings = append([]string(nil), record.Snapshot.Warnings...)
	if creditText, creditOK, creditAmbiguous := usage.SnapshotCreditText(record.Snapshot); creditAmbiguous {
		view.Warnings = append(view.Warnings, "multiple buckets report conflicting credit balances; credits intentionally omitted")
	} else if creditOK {
		view.Credits = creditText
	}
	if five, ok, ambiguous := usage.SnapshotWindowByDuration(record.Snapshot, usage.FiveHourMinutes); ambiguous {
		view.Warnings = append(view.Warnings, "multiple 5H windows are present; 5H display intentionally omitted")
	} else if ok {
		value := five.RemainingPercentDerived
		view.FiveHLeft = &value
	}
	if week, ok, ambiguous := usage.SnapshotWindowByDuration(record.Snapshot, usage.WeeklyMinutes); ambiguous {
		view.Warnings = append(view.Warnings, "multiple weekly windows are present; weekly display intentionally omitted")
	} else if ok {
		value := week.RemainingPercentDerived
		view.WeeklyLeft = &value
	}
	for _, candidate := range usage.SnapshotWindows(record.Snapshot) {
		duration := candidate.Window.WindowDurationMins
		if duration != nil && (*duration == usage.FiveHourMinutes || *duration == usage.WeeklyMinutes) {
			continue
		}
		view.OtherWindows = append(view.OtherWindows, statusWindowView{
			Label:        displayWindowLabel(duration),
			DurationMins: duration,
			Remaining:    candidate.Window.RemainingPercentDerived,
			SourceKey:    candidate.SourceKey,
		})
	}
	key, bucket, ok, ambiguous := usage.ComparableBucket(record.Snapshot)
	if ambiguous {
		view.Warnings = append(view.Warnings, "multiple buckets contain both 5H and weekly windows; selector summary intentionally omitted")
	} else if ok {
		view.BucketKey = key
		if view.Plan == "" && bucket.PlanType != nil {
			view.Plan = *bucket.PlanType
		}
	}
	return view
}

func displayWindowLabel(durationMins *int64) string {
	if durationMins == nil || *durationMins <= 0 {
		return "其它窗口"
	}
	minutes := *durationMins
	switch {
	case minutes%1440 == 0:
		return fmt.Sprintf("%d天", minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf("%d小时", minutes/60)
	default:
		return fmt.Sprintf("%d分钟", minutes)
	}
}

func printStatusTable(w interface{ Write([]byte) (int, error) }, views []statusView) {
	if len(views) == 0 {
		fmt.Fprintln(w, "没有可显示的 CodexPool 账号。")
		return
	}
	fmt.Fprintln(w, "账号名称              默认  状态              5小时剩余  每周剩余   Credits       重置   快照")
	for _, v := range views {
		active := ""
		if v.Active {
			active = "●"
		}
		fmt.Fprintf(w, "%-20s %-4s %-16s %-9s %-9s %-13s %-6s %s\n",
			textsafe.Clean(v.Alias, 20),
			active,
			displayState(statusState(v)),
			percentText(v.FiveHLeft),
			percentText(v.WeeklyLeft),
			textsafe.CleanOr(v.Credits, "—", 40),
			textsafe.CleanOr(v.ResetCredit, "—", 20),
			textsafe.Clean(snapshotText(v), 80),
		)
	}
}

func statusState(v statusView) string {
	if v.Lifecycle != profile.LifecycleReady {
		return string(v.Lifecycle)
	}
	if v.ErrorCode != "" {
		return v.ErrorCode
	}
	if v.UsageStatus != "" {
		return string(v.UsageStatus)
	}
	return "unknown"
}

func snapshotText(v statusView) string {
	if v.FetchedAt.IsZero() {
		if v.ErrorCode != "" {
			return v.ErrorCode
		}
		return "—"
	}
	text := v.FetchedAt.Local().Format("15:04:05")
	if v.ErrorCode != "" {
		text += " · " + v.ErrorCode
	}
	return text
}

func percentText(value *float64) string {
	if value == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", *value)
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func displayPlan(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "—":
		return "未知"
	case "free":
		return "免费"
	case "plus":
		return "Plus"
	case "pro":
		return "Pro"
	case "team":
		return "团队"
	case "enterprise":
		return "企业"
	default:
		return textsafe.Clean(value, 20)
	}
}

func displayStore(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "file":
		return "文件"
	case "keyring":
		return "钥匙串"
	default:
		return textsafe.CleanOr(value, "未知", 20)
	}
}

func displayState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ready":
		return "就绪"
	case "pending":
		return "待登录"
	case "reauthrequired":
		return "需重新登录"
	case "logoutpending":
		return "退出待恢复"
	case "purgelogoutpending":
		return "删除待退出"
	case "deleting":
		return "删除中"
	case "fresh":
		return "数据新鲜"
	case "stale":
		return "数据过期"
	case "unavailable":
		return "暂不可用"
	case "runningcached":
		return "运行中·缓存"
	case "nocache":
		return "暂无缓存"
	case "notauthenticated":
		return "未登录"
	case "notchatgpt":
		return "非 ChatGPT 账号"
	case "identitychanged":
		return "身份已变化"
	case "profilechanged":
		return "Profile 已变化"
	case "unsupportedruntime":
		return "运行时不支持"
	case "protocolincompatible":
		return "协议待兼容"
	case "busy":
		return "正忙"
	case "timeout":
		return "超时"
	case "canceled":
		return "已取消"
	case "unknown", "":
		return "未知"
	default:
		return textsafe.Clean(value, 24)
	}
}

func classifyStatusError(err error) string {
	switch {
	case errors.Is(err, usage.ErrNotAuthenticated):
		return "notAuthenticated"
	case errors.Is(err, usage.ErrNotChatGPT):
		return "notChatGPT"
	case errors.Is(err, usage.ErrIdentityChanged):
		return "identityChanged"
	case errors.Is(err, usage.ErrProtocolIncompatible):
		return "protocolIncompatible"
	case errors.Is(err, codexruntime.ErrProfileChanged):
		return "profileChanged"
	case errors.Is(err, codexruntime.ErrUnsupportedRuntime), errors.Is(err, codexruntime.ErrUnsupportedCredentialStore):
		return "unsupportedRuntime"
	case errors.Is(err, scheduler.ErrBusy):
		return "busy"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "unavailable"
	}
}

func statusExitCode(views []statusView, all bool) int {
	if all || len(views) != 1 {
		return ExitOK
	}
	view := views[0]
	if view.Lifecycle == profile.LifecyclePending || view.Lifecycle == profile.LifecycleReauthRequired {
		return ExitNotLoggedIn
	}
	switch view.ErrorCode {
	case "":
		return ExitOK
	case "notAuthenticated", "notChatGPT", "identityChanged", "profileChanged":
		return ExitNotLoggedIn
	case "unsupportedRuntime":
		return ExitUnsupported
	case "busy":
		return ExitBusy
	default:
		return ExitRuntimeFailure
	}
}

func (a *App) writeJSON(value any) int {
	encoder := json.NewEncoder(a.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return a.fail(ExitRuntimeFailure, err)
	}
	return ExitOK
}

func (a *App) buildTUIRows(ctx context.Context, cachedOnly bool) ([]tui.Row, error) {
	profiles, activeID, err := a.Store.List()
	if err != nil {
		return nil, err
	}
	identityEmail := make(map[string]string, len(profiles))
	for _, p := range profiles {
		identityEmail[p.ID] = p.Identity.Email
	}
	views := a.collectStatus(ctx, profiles, activeID, cachedOnly)
	rows := make([]tui.Row, 0, len(views))
	for _, v := range views {
		other := make([]tui.QuotaWindow, 0, len(v.OtherWindows))
		for _, window := range v.OtherWindows {
			other = append(other, tui.QuotaWindow{
				Label:     window.Label,
				Remaining: fmt.Sprintf("%.0f%%", window.Remaining),
			})
		}
		rows = append(rows, tui.Row{
			ProfileID:      v.ProfileID,
			AuthGeneration: v.AuthGeneration,
			Lifecycle:      string(v.Lifecycle),
			Alias:          v.Alias,
			IdentityEmail:  identityEmail[v.ProfileID],
			Active:         v.Active,
			Plan:           nonEmpty(v.Plan, "—"),
			FiveH:          percentText(v.FiveHLeft),
			Weekly:         percentText(v.WeeklyLeft),
			OtherWindows:   other,
			Credits:        nonEmpty(v.Credits, "—"),
			Resets:         nonEmpty(v.ResetCredit, "—"),
			State:          statusState(v),
		})
	}
	return rows, nil
}

func (a *App) selectorCandidates(ctx context.Context, refresh bool) ([]selector.Candidate, string, error) {
	profiles, activeID, err := a.Store.List()
	if err != nil {
		return nil, "", err
	}
	failedRefresh := make(map[string]struct{})
	if refresh {
		views := a.collectStatus(ctx, profiles, activeID, false)
		for _, view := range views {
			if view.ErrorCode == "unsupportedRuntime" {
				return nil, "", fmt.Errorf("%w：刷新后的候选账号使用了当前 Preview 未验证的运行时或凭据存储方式", codexruntime.ErrUnsupportedRuntime)
			}
			if view.ErrorCode != "" {
				failedRefresh[view.ProfileID] = struct{}{}
			}
		}
	}
	candidates := make([]selector.Candidate, 0, len(profiles))
	for _, p := range profiles {
		if _, failed := failedRefresh[p.ID]; failed {
			continue
		}
		lock, lockErr := a.Runtime.Guard.TryAcquire(p.ID)
		if lockErr != nil {
			continue
		}
		current, resolveErr := a.Store.Resolve(p.ID)
		if resolveErr != nil || !profile.SameCommitIdentity(current, p) || current.Lifecycle != profile.LifecycleReady {
			_ = lock.Close()
			continue
		}
		record, cacheErr := a.Usage.Cached(current)
		_ = lock.Close()
		if cacheErr != nil {
			continue
		}
		candidates = append(candidates, selector.Candidate{
			Profile: current,
			Cache:   record,
		})
	}
	return candidates, activeID, nil
}

func (a *App) commandBest(ctx context.Context, args []string) int {
	jsonMode, refresh, err := parseBestArgs(args)
	if err != nil {
		return a.fail(ExitUsage, err)
	}
	candidates, activeID, err := a.selectorCandidates(ctx, refresh)
	if err != nil {
		return a.fail(ExitRuntimeFailure, err)
	}
	ranked := selector.Rank(candidates, activeID, selectorCacheMaxAge, time.Now())
	if len(ranked) == 0 {
		return a.fail(ExitRuntimeFailure, selector.ErrNoReliableCandidate)
	}
	if jsonMode {
		return a.writeJSON(ranked)
	}
	for i, result := range ranked {
		fmt.Fprintf(a.Stdout, "%d. %s  5小时 %.0f%%  每周 %.0f%%  最低 %.0f%%  bucket=%s  不确定=%t\n",
			i+1, textsafe.Clean(result.Alias, 63), result.FiveHourLeft, result.WeeklyLeft, result.MinimumLeft, textsafe.Clean(result.BucketKey, 80), result.Uncertain)
		if i == 0 {
			for _, reason := range result.Reasons {
				fmt.Fprintln(a.Stdout, "   -", displayBestReason(reason))
			}
		}
	}
	return ExitOK
}

func displayBestReason(reason string) string {
	switch reason {
	case "CodexPool Preview policy keeps automatic account selection disabled; ordinary usage permission is not used for automation":
		return "CodexPool Preview 策略保持自动账号选择禁用，不会依 ordinary usage permission 自动运行或轮换账号"
	case "credits are displayed separately and are not converted into a ranking score":
		return "额度单独展示，不会换算成账号排序分数"
	default:
		return textsafe.Clean(reason, 300)
	}
}

func parseBestArgs(args []string) (bool, bool, error) {
	var jsonMode, refresh bool
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonMode = true
		case "--refresh":
			refresh = true
		default:
			return false, false, fmt.Errorf("未知的 best 选项 %q", arg)
		}
	}
	return jsonMode, refresh, nil
}

func (a *App) commandAuto(ctx context.Context, args []string) int {
	_ = ctx
	_ = args
	return a.fail(ExitUsage, errors.New("v0.1 development preview 已主动禁用 auto 自动切换；请使用 best 查看候选，并用 run <账号名称> -- ... 明确选择账号"))
}
