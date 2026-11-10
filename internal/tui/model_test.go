package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestReloginShortcutKeepsSelectedProfileIdentity(t *testing.T) {
	for _, page := range []pageKind{pageAccounts, pageDetail} {
		rows := []Row{{ProfileID: "p_first", AuthGeneration: 1, Lifecycle: "ready"}, {ProfileID: "p_second", AuthGeneration: 4, Lifecycle: "reauthRequired"}}
		m := newModel(context.Background(), rows, nil)
		m.cursor = 1
		m.page = page
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
		got := updated.(model).action
		if cmd == nil || got.Kind != ActionRelogin || got.Target.ProfileID != "p_second" || got.Target.AuthGeneration != 4 || got.Target.Lifecycle != "reauthRequired" {
			t.Fatalf("relogin must retain selected identity on page %v: %#v", page, got)
		}
	}
}

func TestSanitizeCellRemovesTerminalControlsAndTruncates(t *testing.T) {
	got := sanitizeCell("safe\x1b[31m\nvalue", 9)
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\n') {
		t.Fatalf("terminal controls were not removed: %q", got)
	}
	if got != "safe [31…" {
		t.Fatalf("unexpected sanitized value: %q", got)
	}
}

func TestViewUsesAsitopStyleAccountCardMatrix(t *testing.T) {
	rows := []Row{
		{ProfileID: "p_a", Alias: "账号-A", Lifecycle: "ready", Active: true, Plan: "plus", FiveH: "72%", Weekly: "48%", Credits: "1000", Resets: "0", State: "fresh"},
		{ProfileID: "p_b", Alias: "账号-B", Lifecycle: "ready", Plan: "free", FiveH: "—", Weekly: "—", OtherWindows: []QuotaWindow{{Label: "30天", Remaining: "99%"}}, Credits: "1000", Resets: "0", State: "fresh"},
		{ProfileID: "p_c", Alias: "账号-C", Lifecycle: "ready", Plan: "plus", FiveH: "55%", Weekly: "67%", Credits: "500", Resets: "1", State: "busy"},
		{ProfileID: "p_d", Alias: "账号-D", Lifecycle: "ready", Plan: "pro", FiveH: "90%", Weekly: "80%", Credits: "—", Resets: "0", State: "stale"},
	}
	m := newModel(context.Background(), rows, nil)
	m.width = 118
	m.height = 23
	m.noColor = true
	view := m.View()
	for _, want := range []string{"CodexPool · 账号矩阵", "账号-A", "账号-B", "账号-C", "账号-D", "30天", "99%", "额度 1000", "R 整体刷新"} {
		if !strings.Contains(view, want) {
			t.Fatalf("account matrix missing %q: %q", want, view)
		}
	}
	if strings.Contains(view, "选中账号 · 额度与状态") || strings.Contains(view, "账号列表") {
		t.Fatalf("main screen must be a card matrix, not list/detail split: %q", view)
	}
	if got := strings.Count(view, "┌─"); got != len(rows) {
		t.Fatalf("expected one bordered card per visible account, got %d: %q", got, view)
	}
}

func TestVerifiedIdentityEmailIsPreferredOverLocalAliasInViews(t *testing.T) {
	row := Row{
		ProfileID: "p_a", Alias: "small-02", IdentityEmail: "small-02@example.com",
		Lifecycle: "ready", Active: true, Plan: "free", FiveH: "—", Weekly: "—", Credits: "1000", Resets: "0", State: "fresh",
	}
	m := newModel(context.Background(), []Row{row}, nil)
	m.width, m.height = 118, 23
	accounts := m.View()
	if !strings.Contains(accounts, "small-02@example.com") {
		t.Fatalf("account matrix must prefer the verified email: %q", accounts)
	}

	m.page = pageDetail
	detail := m.View()
	for _, want := range []string{"账号      small-02@example.com", "本地名称  small-02"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("account detail missing %q: %q", want, detail)
		}
	}
}

func TestDesktopActivationRequiresSeparateConfirmationAndKeepsImmutableTarget(t *testing.T) {
	row := Row{ProfileID: "p_a", AuthGeneration: 7, Lifecycle: "ready", Alias: "small", IdentityEmail: "small@example.com"}
	m := newModel(context.Background(), []Row{row}, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	confirm := updated.(model)
	if confirm.page != pageDesktopActivate || confirm.action.Kind != ActionNone {
		t.Fatalf("X must only open confirmation: page=%d action=%#v", confirm.page, confirm.action)
	}
	if !strings.Contains(confirm.View(), "不会直接修改 ChatGPT Desktop") || !strings.Contains(confirm.View(), "small@example.com") {
		t.Fatalf("desktop confirmation lacks safety context: %q", confirm.View())
	}
	updated, _ = confirm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	confirmed := updated.(model)
	if confirmed.action.Kind != ActionDesktopActivate || confirmed.action.Target.ProfileID != row.ProfileID || confirmed.action.Target.AuthGeneration != row.AuthGeneration || confirmed.action.Target.Lifecycle != row.Lifecycle {
		t.Fatalf("desktop action lost immutable target: %#v", confirmed.action)
	}
}

func TestDesktopWarningIsVisibleWithoutChangingAccountSelection(t *testing.T) {
	m := newModelWithDesktop(context.Background(), []Row{{ProfileID: "p_a", Alias: "small"}}, nil, DesktopState{
		Label:   "恢复待处理",
		Warning: "请勿再次激活，先恢复此前 Desktop 登录。",
	})
	m.width, m.height = 118, 23
	view := m.View()
	for _, want := range []string{"Desktop 恢复待处理", "Desktop 提示：请勿再次激活"} {
		if !strings.Contains(view, want) {
			t.Fatalf("desktop warning missing %q: %q", want, view)
		}
	}
}

func TestAccountMatrixAdaptsColumnsAndNeverExceedsTerminalWidth(t *testing.T) {
	rows := []Row{
		{ProfileID: "p_a", Alias: "中文账号A", Lifecycle: "ready", Plan: "plus", FiveH: "72%", Weekly: "48%", Credits: "1000", Resets: "0", State: "fresh"},
		{ProfileID: "p_b", Alias: "中文账号B", Lifecycle: "ready", Plan: "free", FiveH: "—", Weekly: "—", OtherWindows: []QuotaWindow{{Label: "30天", Remaining: "99%"}}, Credits: "1000", Resets: "0", State: "fresh"},
		{ProfileID: "p_c", Alias: "中文账号C", Lifecycle: "ready", Plan: "pro", FiveH: "80%", Weekly: "70%", Credits: "2000", Resets: "0", State: "busy"},
	}
	for _, tc := range []struct {
		name     string
		width    int
		height   int
		wantCols int
	}{
		{name: "wide", width: 140, height: 30, wantCols: 3},
		{name: "medium", width: 100, height: 22, wantCols: 2},
		{name: "narrow", width: 70, height: 18, wantCols: 1},
		{name: "short", width: 70, height: 12, wantCols: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel(context.Background(), rows, nil)
			m.width = tc.width
			m.height = tc.height
			m.noColor = true
			if got := m.gridColumns(); got != tc.wantCols {
				t.Fatalf("grid columns=%d want %d for width=%d", got, tc.wantCols, tc.width)
			}
			view := m.View()
			if strings.Contains(view, "\x1b[") {
				t.Fatalf("NO_COLOR view contains ANSI: %q", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if line == "" {
					continue
				}
				if got := lipgloss.Width(line); got > tc.width {
					t.Fatalf("%s line width=%d exceeds terminal width=%d: %q", tc.name, got, tc.width, line)
				}
			}
		})
	}

	m := newModel(context.Background(), rows, nil)
	m.width = 140
	m.height = 30
	m.noColor = false
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(previousProfile)
	colored := m.View()
	if !strings.Contains(colored, "\x1b[") {
		t.Fatal("colored matrix should contain ANSI styling")
	}
	plain := m
	plain.noColor = true
	if got, want := lipgloss.Width(m.accountCard(rows[0], 46, true)), lipgloss.Width(plain.accountCard(rows[0], 46, true)); got != want {
		t.Fatalf("ANSI account card width=%d, want %d", got, want)
	}
}

func TestAccountFooterAppearsOnceWhenTerminalHeightGrows(t *testing.T) {
	rows := []Row{{
		ProfileID: "p_a", Alias: "p0-xiaoliu5151516", Lifecycle: "ready", Plan: "free",
		FiveH: "—", Weekly: "—", OtherWindows: []QuotaWindow{{Label: "30天", Remaining: "99%"}}, Credits: "1000", Resets: "0", State: "fresh",
	}}
	for _, height := range []int{20, 30, 40} {
		m := newModel(context.Background(), rows, nil)
		m.width = 105
		m.height = height
		m.noColor = true
		view := m.View()
		if got := strings.Count(view, "HJKL 选择账号"); got != 1 {
			t.Fatalf("height=%d footer count=%d want 1:\n%s", height, got, view)
		}
		lines := strings.Split(view, "\n")
		if got := len(lines); got != height {
			t.Fatalf("height=%d rendered lines=%d want %d", height, got, height)
		}
		for _, line := range lines {
			if got := lipgloss.Width(line); got > m.width {
				t.Fatalf("height=%d line width=%d exceeds terminal width=%d: %q", height, got, m.width, line)
			}
		}
	}
}

func TestFreeAccountCardShowsNonstandardWindowCreditsAndUnknownStandardWindows(t *testing.T) {
	m := newModel(context.Background(), []Row{{
		ProfileID: "p_a", Alias: "free-small", Lifecycle: "ready", Plan: "free",
		FiveH: "—", Weekly: "—", OtherWindows: []QuotaWindow{{Label: "30天", Remaining: "99%"}}, Credits: "1000", Resets: "0", State: "fresh",
	}}, nil)
	m.noColor = true
	card := m.accountCard(m.rows[0], 46, true)
	for _, want := range []string{"5小时", "每周", "未提供", "30天", "99%", "额度 1000"} {
		if !strings.Contains(card, want) {
			t.Fatalf("free account card missing %q: %q", want, card)
		}
	}
}

func TestAccountCardQuotaRowsAlignAtMac118x13(t *testing.T) {
	m := newModel(context.Background(), []Row{{
		ProfileID: "p_a", Alias: "p0-xiaoliu5151516", Lifecycle: "ready", Plan: "free",
		FiveH: "—", Weekly: "—", OtherWindows: []QuotaWindow{{Label: "30天", Remaining: "99%"}}, Credits: "1000", Resets: "0", State: "fresh",
	}}, nil)
	m.width = 118
	m.height = 13
	m.noColor = true
	cardWidth := gridColumnWidths(m.width, m.gridColumns())[0]
	card := m.accountCard(m.rows[0], cardWidth, true)

	var fiveLine, weekLine, monthLine string
	for _, line := range strings.Split(card, "\n") {
		switch {
		case strings.Contains(line, "5小时"):
			fiveLine = line
		case strings.Contains(line, "每周"):
			weekLine = line
		case strings.Contains(line, "30天"):
			monthLine = line
		}
		if got := lipgloss.Width(line); got != cardWidth {
			t.Fatalf("card line width=%d want %d: %q", got, cardWidth, line)
		}
	}
	if fiveLine == "" || weekLine == "" || monthLine == "" {
		t.Fatalf("missing quota rows in card: %q", card)
	}
	column := func(line, needle string) int {
		t.Helper()
		idx := strings.Index(line, needle)
		if idx < 0 {
			t.Fatalf("%q missing from %q", needle, line)
		}
		return lipgloss.Width(line[:idx])
	}
	fiveBar := column(fiveLine, "░")
	weekBar := column(weekLine, "░")
	monthBar := column(monthLine, "█")
	if fiveBar != weekBar || fiveBar != monthBar {
		t.Fatalf("quota bars are not vertically aligned: 5H=%d weekly=%d 30d=%d\n%s", fiveBar, weekBar, monthBar, card)
	}
	fiveValue := column(fiveLine, "未提供")
	weekValue := column(weekLine, "未提供")
	monthValue := column(monthLine, "99%")
	if fiveValue != weekValue || fiveValue != monthValue {
		t.Fatalf("quota values are not vertically aligned: 5H=%d weekly=%d 30d=%d\n%s", fiveValue, weekValue, monthValue, card)
	}
}

func TestGridCursorMovesInTwoDimensionsWithoutWrappingRows(t *testing.T) {
	if got := moveGridCursor(0, 7, 3, 1, 0); got != 1 {
		t.Fatalf("right move=%d want 1", got)
	}
	if got := moveGridCursor(2, 7, 3, 1, 0); got != 2 {
		t.Fatalf("right must not wrap into next row, got %d", got)
	}
	if got := moveGridCursor(1, 7, 3, 0, 1); got != 4 {
		t.Fatalf("down move=%d want 4", got)
	}
	if got := moveGridCursor(5, 7, 3, 0, 1); got != 6 {
		t.Fatalf("down into partial final row should choose closest card, got %d", got)
	}
}

func TestDetailPageIsChineseAndShowsNonstandardQuotaWindows(t *testing.T) {
	m := newModel(context.Background(), []Row{{
		ProfileID: "p_a", AuthGeneration: 7, Lifecycle: "ready", Alias: "主账号", Active: true,
		Plan: "free", FiveH: "—", Weekly: "—", OtherWindows: []QuotaWindow{{Label: "30天", Remaining: "99%"}}, Credits: "1000", Resets: "0", State: "fresh",
	}}, nil)
	m.width = 110
	m.height = 28
	m.noColor = true
	m.page = pageDetail
	view := m.View()
	for _, want := range []string{"CodexPool · 账号详情", "账号信息", "额度监控", "30天", "99%", "额度       1000", "安全说明", "Enter/O 启动官方 Codex", "目录信任", "可能显示英文", "不修改、不翻译也不绕过"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail page missing %q: %q", want, view)
		}
	}
}

func TestParsePercentAndChineseLabels(t *testing.T) {
	if got, ok := parsePercent("99%"); !ok || got != 99 {
		t.Fatalf("parsePercent(99%%) = %v, %t", got, ok)
	}
	if _, ok := parsePercent("—"); ok {
		t.Fatal("unknown percentage must remain unknown")
	}
	if localizePlan("free") != "免费" || localizeState("reauthRequired") != "需重新登录" {
		t.Fatal("Chinese plan/state localization mismatch")
	}
}

func TestKeyboardActionsBindOnlyTheSelectedImmutableProfileTarget(t *testing.T) {
	base := newModel(context.Background(), []Row{
		{ProfileID: "p_a", AuthGeneration: 7, Lifecycle: "ready", Alias: "a"},
		{ProfileID: "p_b", AuthGeneration: 9, Lifecycle: "ready", Alias: "b"},
		{ProfileID: "p_c", AuthGeneration: 11, Lifecycle: "ready", Alias: "c"},
		{ProfileID: "p_d", AuthGeneration: 13, Lifecycle: "ready", Alias: "d"},
	}, nil)
	base.width = 118

	updated, _ := base.Update(tea.KeyMsg{Type: tea.KeyRight})
	moved := updated.(model)
	if moved.cursor != 1 {
		t.Fatalf("right key did not move to the next account card: %d", moved.cursor)
	}

	updated, cmd := moved.Update(tea.KeyMsg{Type: tea.KeyEnter})
	detail := updated.(model)
	if detail.page != pageDetail || detail.action.Kind != ActionNone || cmd != nil {
		t.Fatalf("first enter must open the localized detail page without launching Codex: page=%v action=%#v cmd=%v", detail.page, detail.action, cmd)
	}

	updated, cmd = detail.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runModel := updated.(model)
	if runModel.action.Kind != ActionRun || runModel.action.Target.ProfileID != "p_b" || runModel.action.Target.AuthGeneration != 9 || cmd == nil {
		t.Fatalf("detail enter must bind only the selected immutable target: %#v", runModel.action)
	}

	updated, _ = detail.Update(tea.KeyMsg{Type: tea.KeyEsc})
	back := updated.(model)
	if back.page != pageAccounts || back.cursor != 1 {
		t.Fatalf("escape must return to the same selected account card, page=%v cursor=%d", back.page, back.cursor)
	}

	updated, _ = moved.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	useModel := updated.(model)
	if useModel.action.Kind != ActionUse || useModel.action.Target.ProfileID != "p_b" || useModel.action.Target.AuthGeneration != 9 {
		t.Fatalf("u must bind only the selected account card: %#v", useModel.action)
	}
}

func TestRefreshPreservesSelectedProfileIDAcrossRenameAndSanitizesError(t *testing.T) {
	m := newModel(context.Background(), []Row{{ProfileID: "p_a", Alias: "a"}, {ProfileID: "p_b", Alias: "b"}}, nil)
	m.cursor = 1
	updated, _ := m.Update(refreshedMsg{
		rows: []Row{{ProfileID: "p_b", Alias: "renamed"}, {ProfileID: "p_c", Alias: "c"}},
		at:   time.Unix(1900000000, 0),
		seq:  0,
	})
	refreshed := updated.(model)
	if refreshed.cursor != 0 || refreshed.selectedProfileID() != "p_b" || refreshed.rows[0].Alias != "renamed" {
		t.Fatalf("refresh did not preserve selected Profile ID across rename: cursor=%d id=%q rows=%#v", refreshed.cursor, refreshed.selectedProfileID(), refreshed.rows)
	}

	updated, _ = refreshed.Update(refreshedMsg{err: &testError{text: "bad\x1b[2J\nerror"}, seq: 0})
	withErr := updated.(model)
	if strings.ContainsRune(withErr.errText, '\x1b') || strings.ContainsRune(withErr.errText, '\n') {
		t.Fatalf("refresh error was not sanitized: %q", withErr.errText)
	}
}

func TestDetailPageReturnsToMatrixIfSelectedProfileDisappearsOnRefresh(t *testing.T) {
	m := newModel(context.Background(), []Row{{ProfileID: "p_a", Alias: "a"}, {ProfileID: "p_b", Alias: "b"}}, nil)
	m.cursor = 1
	m.page = pageDetail
	updated, _ := m.Update(refreshedMsg{
		rows: []Row{{ProfileID: "p_a", Alias: "a"}, {ProfileID: "p_c", Alias: "c"}},
		at:   time.Unix(1900000000, 0),
		seq:  0,
	})
	refreshed := updated.(model)
	if refreshed.page != pageAccounts || refreshed.cursor != 0 || refreshed.selectedProfileID() != "p_a" {
		t.Fatalf("disappeared detail target must return safely to matrix: page=%v cursor=%d id=%q", refreshed.page, refreshed.cursor, refreshed.selectedProfileID())
	}
}

func TestRefreshRequestsCoalesceAndStaleResultsCannotOverwrite(t *testing.T) {
	calls := 0
	refresh := func(context.Context) ([]Row, error) {
		calls++
		return []Row{{ProfileID: "p_a", Alias: "fresh"}}, nil
	}
	m := newModel(context.Background(), []Row{{ProfileID: "p_a", Alias: "old"}}, refresh)
	m, first := m.requestRefresh(false)
	if first == nil || !m.refreshInFlight || m.refreshSeq != 1 {
		t.Fatalf("first refresh was not started correctly: %#v", m)
	}
	m, second := m.requestRefresh(false)
	if second != nil || !m.refreshPending || m.refreshSeq != 1 {
		t.Fatalf("overlapping refresh should be coalesced: %#v", m)
	}
	msg := first().(refreshedMsg)
	updated, next := m.Update(msg)
	m = updated.(model)
	if next == nil || !m.refreshInFlight || m.refreshSeq != 2 || m.refreshPending {
		t.Fatalf("one pending refresh should start after the first finishes: %#v", m)
	}
	staleRows := append([]Row(nil), m.rows...)
	updated, _ = m.Update(refreshedMsg{seq: 1, rows: []Row{{ProfileID: "p_wrong", Alias: "wrong"}}})
	m = updated.(model)
	if len(m.rows) != len(staleRows) || m.rows[0].ProfileID != staleRows[0].ProfileID {
		t.Fatalf("stale refresh result overwrote newer batch state: %#v", m.rows)
	}
	_ = next()
	if calls != 2 {
		t.Fatalf("expected exactly two coalesced refresh calls, got %d", calls)
	}
}

func TestManualRefreshShowsImmediateAndCompletionFeedback(t *testing.T) {
	m := newModel(context.Background(), []Row{{ProfileID: "p_a", Alias: "a"}}, func(context.Context) ([]Row, error) {
		return []Row{{ProfileID: "p_a", Alias: "fresh"}}, nil
	})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(model)
	if cmd == nil || !strings.Contains(m.refreshNote, "已收到 R") || !strings.Contains(m.View(), "已收到 R") {
		t.Fatalf("R must acknowledge manual refresh immediately: note=%q view=%q", m.refreshNote, m.View())
	}
	updated, _ = m.Update(cmd().(refreshedMsg))
	m = updated.(model)
	if !strings.Contains(m.refreshNote, "R 刷新完成") || !strings.Contains(m.View(), "R 刷新完成") {
		t.Fatalf("successful manual refresh feedback missing: note=%q view=%q", m.refreshNote, m.View())
	}
}

func TestAutomaticRefreshShowsBackgroundFeedback(t *testing.T) {
	m := newModel(context.Background(), []Row{{ProfileID: "p_a", Alias: "a"}}, func(context.Context) ([]Row, error) {
		return []Row{{ProfileID: "p_a", Alias: "fresh"}}, nil
	})
	updated, cmd := m.Update(m.Init()())
	m = updated.(model)
	if cmd == nil || !m.refreshInFlight || !strings.Contains(m.refreshNote, "后台刷新：正在") {
		t.Fatalf("opening the panel must start an identified automatic refresh: %#v", m)
	}
	updated, _ = m.Update(refreshedMsg{seq: m.refreshSeq, rows: m.rows, at: time.Unix(1900000000, 0)})
	m = updated.(model)
	if !strings.Contains(m.refreshNote, "后台刷新完成") {
		t.Fatalf("automatic completion feedback missing: %q", m.refreshNote)
	}
	updated, cmd = m.Update(tickMsg(time.Now()))
	m = updated.(model)
	if cmd == nil || !m.refreshInFlight || !strings.Contains(m.refreshNote, "后台刷新：正在") {
		t.Fatalf("60-second refresh must restart with feedback: %#v", m)
	}
}

func TestCancelRefreshPropagatesContext(t *testing.T) {
	started := make(chan struct{})
	refresh := func(ctx context.Context) ([]Row, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	m := newModel(context.Background(), nil, refresh)
	m, cmd := m.requestRefresh(false)
	result := make(chan refreshedMsg, 1)
	go func() { result <- cmd().(refreshedMsg) }()
	<-started
	m.cancelRefresh()
	select {
	case msg := <-result:
		if !errors.Is(msg.err, context.Canceled) {
			t.Fatalf("refresh did not observe cancellation: %v", msg.err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled refresh did not return")
	}
}

type testError struct{ text string }

func (e *testError) Error() string { return e.text }
