package tui

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"codexpool/internal/textsafe"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type ActionKind string

const (
	ActionNone            ActionKind = ""
	ActionRun             ActionKind = "run"
	ActionUse             ActionKind = "use"
	ActionAdd             ActionKind = "add"
	ActionRelogin         ActionKind = "relogin"
	ActionRemove          ActionKind = "remove"
	ActionDesktopActivate ActionKind = "desktopActivate"
	ActionDesktopRestore  ActionKind = "desktopRestore"
)

type ActionTarget struct {
	ProfileID      string
	AuthGeneration uint64
	Lifecycle      string
}

type Action struct {
	Kind   ActionKind
	Target ActionTarget
}

type DesktopState struct {
	Label   string
	Warning string
}

type pageKind uint8

const (
	pageAccounts pageKind = iota
	pageDetail
	pageDesktopActivate
	pageDesktopRestore
)

type QuotaWindow struct {
	Label     string
	Remaining string
}

type Row struct {
	ProfileID      string
	AuthGeneration uint64
	Lifecycle      string
	Alias          string
	IdentityEmail  string
	Active         bool
	Plan           string
	FiveH          string
	Weekly         string
	OtherWindows   []QuotaWindow
	Credits        string
	Resets         string
	State          string
}

type RefreshFunc func(context.Context) ([]Row, error)

type model struct {
	ctx             context.Context
	rows            []Row
	cursor          int
	width           int
	height          int
	lastRefresh     time.Time
	refreshNote     string
	errText         string
	refresh         RefreshFunc
	action          Action
	page            pageKind
	noColor         bool
	refreshSeq      uint64
	refreshInFlight bool
	refreshPending  bool
	refreshManual   bool
	pendingManual   bool
	refreshCancel   context.CancelFunc
	refreshDone     <-chan struct{}
	desktop         DesktopState
}

type refreshedMsg struct {
	rows []Row
	err  error
	at   time.Time
	seq  uint64
}

type refreshRequestMsg struct{}

type tickMsg time.Time

func newModel(ctx context.Context, rows []Row, refresh RefreshFunc) model {
	if ctx == nil {
		ctx = context.Background()
	}
	return model{
		ctx:     ctx,
		rows:    append([]Row(nil), rows...),
		width:   100,
		height:  30,
		refresh: refresh,
		noColor: os.Getenv("NO_COLOR") != "",
	}
}

func newModelWithDesktop(ctx context.Context, rows []Row, refresh RefreshFunc, desktop DesktopState) model {
	m := newModel(ctx, rows, refresh)
	m.desktop = desktop
	return m
}

func (m model) Init() tea.Cmd {
	return func() tea.Msg { return refreshRequestMsg{} }
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := msg.String()
		if key == "ctrl+c" || key == "q" {
			m.cancelRefresh()
			return m, tea.Quit
		}
		if m.page == pageDesktopActivate || m.page == pageDesktopRestore {
			switch key {
			case "esc", "b", "left":
				m.page = pageAccounts
			case "enter":
				if m.page == pageDesktopActivate {
					if target, ok := m.selectedTarget(); ok {
						m.action = Action{Kind: ActionDesktopActivate, Target: target}
						m.cancelRefresh()
						return m, tea.Quit
					}
				} else {
					m.action = Action{Kind: ActionDesktopRestore}
					m.cancelRefresh()
					return m, tea.Quit
				}
			}
			return m, nil
		}
		if key == "c" {
			if target, ok := m.selectedTarget(); ok {
				m.action = Action{Kind: ActionRelogin, Target: target}
				m.cancelRefresh()
				return m, tea.Quit
			}
			return m, nil
		}
		if m.page == pageDetail {
			switch key {
			case "esc", "b", "left":
				m.page = pageAccounts
				return m, nil
			case "r":
				return m.manualRefresh()
			case "enter", "o":
				if target, ok := m.selectedTarget(); ok {
					m.action = Action{Kind: ActionRun, Target: target}
					m.cancelRefresh()
					return m, tea.Quit
				}
			case "u":
				if target, ok := m.selectedTarget(); ok {
					m.action = Action{Kind: ActionUse, Target: target}
					m.cancelRefresh()
					return m, tea.Quit
				}
			case "d":
				if target, ok := m.selectedTarget(); ok {
					m.action = Action{Kind: ActionRemove, Target: target}
					m.cancelRefresh()
					return m, tea.Quit
				}
			}
			return m, nil
		}
		switch key {
		case "up", "k":
			m.cursor = moveGridCursor(m.cursor, len(m.rows), m.gridColumns(), 0, -1)
		case "down", "j":
			m.cursor = moveGridCursor(m.cursor, len(m.rows), m.gridColumns(), 0, 1)
		case "left", "h":
			m.cursor = moveGridCursor(m.cursor, len(m.rows), m.gridColumns(), -1, 0)
		case "right", "l":
			m.cursor = moveGridCursor(m.cursor, len(m.rows), m.gridColumns(), 1, 0)
		case "r":
			return m.manualRefresh()
		case "enter":
			if _, ok := m.selectedTarget(); ok {
				m.page = pageDetail
			}
		case "o":
			if target, ok := m.selectedTarget(); ok {
				m.action = Action{Kind: ActionRun, Target: target}
				m.cancelRefresh()
				return m, tea.Quit
			}
		case "u":
			if target, ok := m.selectedTarget(); ok {
				m.action = Action{Kind: ActionUse, Target: target}
				m.cancelRefresh()
				return m, tea.Quit
			}
		case "a":
			m.action = Action{Kind: ActionAdd}
			m.cancelRefresh()
			return m, tea.Quit
		case "d":
			if target, ok := m.selectedTarget(); ok {
				m.action = Action{Kind: ActionRemove, Target: target}
				m.cancelRefresh()
				return m, tea.Quit
			}
		case "x":
			if _, ok := m.selectedTarget(); ok {
				m.page = pageDesktopActivate
			}
		case "v":
			m.page = pageDesktopRestore
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case refreshRequestMsg:
		m, refresh := m.automaticRefresh()
		return m, tea.Batch(refresh, tickCmd())
	case tickMsg:
		m, refresh := m.automaticRefresh()
		return m, tea.Batch(refresh, tickCmd())
	case refreshedMsg:
		if msg.seq != m.refreshSeq {
			return m, nil
		}
		m.refreshInFlight = false
		m.refreshCancel = nil
		m.refreshDone = nil
		manual := m.refreshManual
		if msg.err != nil {
			m.errText = sanitizeCell(msg.err.Error(), 120)
			if manual {
				m.refreshNote = "R 刷新失败"
			} else {
				m.refreshNote = "后台刷新失败"
			}
		} else {
			selected := m.selectedProfileID()
			m.rows = append([]Row(nil), msg.rows...)
			m.lastRefresh = msg.at
			if manual {
				m.refreshNote = "R 刷新完成 " + msg.at.Local().Format("15:04:05")
			} else {
				m.refreshNote = "后台刷新完成 " + msg.at.Local().Format("15:04:05")
			}
			m.errText = ""
			m.cursor = indexOfProfileID(m.rows, selected)
			if m.cursor < 0 {
				m.cursor = 0
				if m.page == pageDetail {
					m.page = pageAccounts
				}
			}
		}
		if m.refreshPending {
			nextManual := m.pendingManual
			m.refreshPending = false
			m.pendingManual = false
			if nextManual {
				m.errText = ""
				m.refreshNote = "已收到 R：正在刷新全部账号…"
			} else {
				m.refreshNote = "后台刷新：正在刷新全部账号…"
			}
			return m.requestRefresh(nextManual)
		}
	}
	return m, nil
}

func (m model) manualRefresh() (model, tea.Cmd) {
	m.errText = ""
	m.refreshNote = "已收到 R：正在刷新全部账号…"
	return m.requestRefresh(true)
}

func (m model) automaticRefresh() (model, tea.Cmd) {
	if !m.refreshInFlight {
		m.refreshNote = "后台刷新：正在刷新全部账号…"
	}
	return m.requestRefresh(false)
}

func tickCmd() tea.Cmd {
	return tea.Tick(60*time.Second, func(at time.Time) tea.Msg { return tickMsg(at) })
}

func (m model) requestRefresh(manual bool) (model, tea.Cmd) {
	if m.refresh == nil {
		return m, nil
	}
	if m.refreshInFlight {
		m.refreshPending = true
		if manual {
			m.pendingManual = true
		}
		return m, nil
	}
	m.refreshSeq++
	m.refreshInFlight = true
	m.refreshPending = false
	m.refreshManual = manual
	m.pendingManual = false
	refreshCtx, cancel := context.WithCancel(m.ctx)
	done := make(chan struct{})
	m.refreshCancel = cancel
	m.refreshDone = done
	return m, refreshCmd(refreshCtx, m.refresh, m.refreshSeq, done)
}

func (m *model) cancelRefresh() {
	m.refreshPending = false
	m.pendingManual = false
	if m.refreshCancel != nil {
		m.refreshCancel()
	}
}

func (m model) View() string {
	if m.page == pageDetail {
		return m.detailView()
	}
	if m.page == pageDesktopActivate || m.page == pageDesktopRestore {
		return m.desktopConfirmView()
	}
	return m.accountsView()
}

func (m model) desktopConfirmView() string {
	width, height := m.viewport()
	activate := m.page == pageDesktopActivate
	title := "Desktop 恢复确认"
	lines := []string{
		"此界面不会直接修改 ChatGPT Desktop。",
		"确认后只会在 Terminal 输出独立 helper 指令。",
		"执行 helper 前，仍必须由你本人按 Cmd+Q 完整退出 Desktop。",
	}
	if activate {
		title = "Desktop 临时激活确认"
		if row, ok := m.selectedRow(); ok {
			lines = append([]string{"目标账号  " + displayAccountName(row, 48)}, lines...)
		}
		lines = append(lines, "完成后必须使用恢复流程回到此前 Desktop 登录。")
	} else {
		lines = append(lines, "只有存在此前 helper 创建的恢复记录时，恢复才会执行。")
	}
	lines = append(lines, "Enter 显示指令   B/Esc 取消")
	var b strings.Builder
	b.WriteString(m.topBar(width, false))
	b.WriteByte('\n')
	if m.desktop.Warning != "" {
		b.WriteString(fitDisplay("Desktop 提示："+sanitizeCell(m.desktop.Warning, 100), width))
		b.WriteByte('\n')
	}
	if m.refreshNote != "" {
		b.WriteString(fitDisplay(m.refreshNote, width))
		b.WriteByte('\n')
	}
	b.WriteString(m.panel(title, wrapDisplay(strings.Join(lines, "\n"), width-4), width))
	return fillViewportBelow(b.String(), width, height)
}

func (m model) accountsView() string {
	width, height := m.viewport()
	var b strings.Builder
	b.WriteString(m.topBar(width, false))
	b.WriteByte('\n')
	if m.desktop.Warning != "" {
		b.WriteString(fitDisplay("Desktop 提示："+sanitizeCell(m.desktop.Warning, 100), width))
		b.WriteByte('\n')
	}
	if m.refreshNote != "" {
		b.WriteString(fitDisplay(m.refreshNote, width))
		b.WriteByte('\n')
	}

	if len(m.rows) == 0 {
		body := []string{"还没有账号。", "按 A 添加账号，并通过官方 Codex 登录流程完成认证。"}
		b.WriteString(m.panel("账号矩阵", body, width))
		b.WriteByte('\n')
		b.WriteString(m.accountsFooter(width, true))
		return fillViewportBelow(b.String(), width, height)
	}

	cols := m.gridColumns()
	columnWidths := gridColumnWidths(width, cols)
	cardHeight := 8
	reservedLines := 2
	if m.desktop.Warning != "" {
		reservedLines++
	}
	if m.refreshNote != "" {
		reservedLines++
	}
	if m.errText != "" {
		reservedLines++
	}
	gridRows := (height - reservedLines + 1) / (cardHeight + 1)
	if gridRows < 1 {
		gridRows = 1
	}
	perPage := cols * gridRows
	start, end := gridPageRange(len(m.rows), m.cursor, perPage)
	for rowStart := start; rowStart < end; rowStart += cols {
		rowEnd := rowStart + cols
		if rowEnd > end {
			rowEnd = end
		}
		cards := make([]string, 0, rowEnd-rowStart)
		for index := rowStart; index < rowEnd; index++ {
			column := index - rowStart
			cards = append(cards, m.accountCard(m.rows[index], columnWidths[column], index == m.cursor))
		}
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, intersperseCards(cards)...))
		b.WriteByte('\n')
	}

	if m.errText != "" {
		errLine := fitDisplay("刷新失败："+m.errText, width)
		if !m.noColor {
			errLine = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(errLine)
		}
		b.WriteString(errLine)
		b.WriteByte('\n')
	}
	footer := m.accountsFooter(width, false)
	if start > 0 || end < len(m.rows) {
		footer = fmt.Sprintf("%s   │ 显示 %d-%d / %d", footer, start+1, end, len(m.rows))
	}
	b.WriteString(fitDisplay(footer, width))
	return fillViewportBelow(b.String(), width, height)
}

func (m model) accountCard(row Row, width int, selected bool) string {
	if width < 24 {
		width = 24
	}
	inner := width - 2
	marker := "  "
	if selected {
		marker = "▶ "
	}
	defaultMark := ""
	if row.Active {
		defaultMark = "● "
	}
	title := marker + defaultMark + displayAccountName(row, 28)
	label := " " + title + " "
	fill := inner - lipgloss.Width(label) - 1
	if fill < 0 {
		label = " " + fitDisplay(title, inner-4) + " "
		fill = inner - lipgloss.Width(label) - 1
		if fill < 0 {
			fill = 0
		}
	}
	top := "┌─" + label + strings.Repeat("─", fill) + "┐"
	bottom := "└" + strings.Repeat("─", inner) + "┘"

	barWidth := inner - 19
	if barWidth < 8 {
		barWidth = 8
	}
	if barWidth > 18 {
		barWidth = 18
	}
	const quotaLabelWidth = 8
	lines := []string{
		fmt.Sprintf("套餐 %-6s  状态 %s", localizePlan(row.Plan), localizeState(row.State)),
		m.quotaRow("5小时", row.FiveH, barWidth, quotaLabelWidth),
		m.quotaRow("每周", row.Weekly, barWidth, quotaLabelWidth),
	}
	for i := 0; i < 2; i++ {
		if i < len(row.OtherWindows) {
			window := row.OtherWindows[i]
			lines = append(lines, m.quotaRow(window.Label, window.Remaining, barWidth, quotaLabelWidth))
		} else if i == 0 && len(row.OtherWindows) == 0 {
			lines = append(lines, padDisplay("其它窗口", quotaLabelWidth)+"  —")
		} else {
			lines = append(lines, "")
		}
	}
	if len(row.OtherWindows) > 2 {
		lines[4] = fmt.Sprintf("%s  (+%d 个窗口)", lines[4], len(row.OtherWindows)-2)
	}
	lines = append(lines, fmt.Sprintf("额度 %s   重置 %s", sanitizeCell(row.Credits, 18), sanitizeCell(row.Resets, 10)))

	var b strings.Builder
	b.WriteString(m.cardBorder(top, selected))
	b.WriteByte('\n')
	for _, line := range lines {
		b.WriteString(m.cardBorder("│", selected))
		b.WriteString(fitDisplay(" "+line, inner))
		b.WriteString(m.cardBorder("│", selected))
		b.WriteByte('\n')
	}
	b.WriteString(m.cardBorder(bottom, selected))
	return b.String()
}

// displayAccountName prefers the identity verified by the official Codex
// account/read response. Alias remains the stable local command handle and is
// used until a verified identity is available.
func displayAccountName(row Row, max int) string {
	if email := sanitizeCell(row.IdentityEmail, max); email != "" {
		return email
	}
	return sanitizeCell(row.Alias, max)
}

func (m model) cardBorder(value string, selected bool) string {
	if m.noColor || !selected {
		return value
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")).Render(value)
}

func (m model) gridColumns() int {
	width, _ := m.viewport()
	const minCardWidth = 38
	cols := (width + 1) / (minCardWidth + 1)
	if cols < 1 {
		return 1
	}
	return cols
}

func gridColumnWidths(width, cols int) []int {
	if cols < 1 {
		cols = 1
	}
	usable := width - (cols - 1)
	if usable < cols*24 {
		usable = cols * 24
	}
	base := usable / cols
	remainder := usable % cols
	widths := make([]int, cols)
	for i := range widths {
		widths[i] = base
		if i < remainder {
			widths[i]++
		}
	}
	return widths
}

func intersperseCards(cards []string) []string {
	if len(cards) <= 1 {
		return cards
	}
	parts := make([]string, 0, len(cards)*2-1)
	for i, card := range cards {
		if i > 0 {
			parts = append(parts, " ")
		}
		parts = append(parts, card)
	}
	return parts
}

func gridPageRange(total, cursor, perPage int) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	if perPage < 1 {
		perPage = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		cursor = total - 1
	}
	start := (cursor / perPage) * perPage
	end := start + perPage
	if end > total {
		end = total
	}
	return start, end
}

func moveGridCursor(cursor, total, cols, dx, dy int) int {
	if total <= 0 {
		return 0
	}
	if cols < 1 {
		cols = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		cursor = total - 1
	}
	row := cursor / cols
	col := cursor % cols
	if dx != 0 {
		candidate := cursor + dx
		if candidate >= 0 && candidate < total && candidate/cols == row {
			return candidate
		}
		return cursor
	}
	if dy != 0 {
		newRow := row + dy
		if newRow < 0 {
			return cursor
		}
		candidate := newRow*cols + col
		if candidate >= total {
			lastRow := (total - 1) / cols
			if newRow != lastRow {
				return cursor
			}
			candidate = total - 1
		}
		return candidate
	}
	return cursor
}

func (m model) detailView() string {
	width, height := m.viewport()
	row, ok := m.selectedRow()
	if !ok {
		m.page = pageAccounts
		return m.accountsView()
	}
	var b strings.Builder
	b.WriteString(m.topBar(width, true))
	b.WriteByte('\n')
	if m.refreshNote != "" {
		b.WriteString(fitDisplay(m.refreshNote, width))
		b.WriteByte('\n')
	}

	infoLines := []string{
		"账号      " + displayAccountName(row, 40),
		"套餐      " + localizePlan(row.Plan),
		"状态      " + localizeState(row.State),
		"默认账号  " + yesNo(row.Active),
	}
	if row.IdentityEmail != "" {
		infoLines = append(infoLines, "本地名称  "+sanitizeCell(row.Alias, 40))
	}
	if height >= 20 {
		infoLines = append(infoLines, "账号 ID   "+sanitizeCell(row.ProfileID, 60))
	}
	quotaLines := m.quotaLines(row, width)

	if width >= 104 && height >= 20 {
		leftWidth := width * 42 / 100
		rightWidth := width - leftWidth - 1
		left := m.panel("账号信息", fillLines(infoLines, 8), leftWidth)
		right := m.panel("额度监控", fillLines(quotaLines, 8), rightWidth)
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right))
		b.WriteByte('\n')
	} else {
		b.WriteString(m.panel("账号信息", infoLines, width))
		b.WriteByte('\n')
		b.WriteString(m.panel("额度监控", quotaLines, width))
		b.WriteByte('\n')
	}

	noteWidth := width - 4
	note := "启动后将进入 OpenAI 官方 Codex CLI。官方“目录信任”等安全提示由上游 Codex 提供，当前版本可能显示英文；CodexPool 不修改、不翻译也不绕过这些安全提示。"
	if row, ok := m.selectedRow(); ok && row.Lifecycle == "reauthRequired" {
		note = "该账号需要重新登录，重复刷新无法恢复登录。按 C 启动官方登录流程，并在浏览器中登录此账号；完成后重新打开面板即可查看额度。"
	}
	if height >= 18 {
		b.WriteString(m.panel("安全说明", wrapDisplay(note, noteWidth), width))
		b.WriteByte('\n')
	}
	b.WriteString(m.detailFooter(width))
	return fillViewportBelow(b.String(), width, height)
}

func (m model) topBar(width int, detail bool) string {
	activeName := "未设置"
	ready := 0
	for _, row := range m.rows {
		if row.Active {
			activeName = displayAccountName(row, 20)
		}
		if row.Lifecycle == "ready" {
			ready++
		}
	}
	refreshText := "尚未刷新"
	if !m.lastRefresh.IsZero() {
		refreshText = m.lastRefresh.Local().Format("15:04:05")
	}
	refreshState := "空闲"
	if m.refreshInFlight {
		refreshState = "刷新中"
	}
	title := "CodexPool · 账号矩阵"
	if detail {
		title = "CodexPool · 账号详情"
	}
	desktop := "Desktop 无临时切换记录"
	if m.desktop.Label != "" {
		desktop = "Desktop " + sanitizeCell(m.desktop.Label, 28)
	}
	plain := fmt.Sprintf("%s  │ 账号 %d  就绪 %d  默认 %s  │ %s %s  │ %s  │ 手动刷新 R", title, len(m.rows), ready, activeName, refreshState, refreshText, desktop)
	if width < 82 {
		plain = fmt.Sprintf("%s  │ %d账号 · %d就绪 · %s", title, len(m.rows), ready, refreshState)
	}
	fitted := fitDisplay(plain, width)
	if m.noColor || !strings.Contains(fitted, title) {
		return fitted
	}
	coloredTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")).Render(title)
	return strings.Replace(fitted, title, coloredTitle, 1)
}

func (m model) accountListLines(maxRows, contentWidth int) []string {
	if maxRows < 1 {
		maxRows = 1
	}
	start, end := visibleRange(len(m.rows), m.cursor, maxRows)
	lines := make([]string, 0, maxRows)
	for i := start; i < end; i++ {
		row := m.rows[i]
		marker := "  "
		selected := i == m.cursor
		switch {
		case selected:
			marker = "▶ "
		case i == start && start > 0:
			marker = "↑ "
		case i == end-1 && end < len(m.rows):
			marker = "↓ "
		}
		defaultMark := " "
		if row.Active {
			defaultMark = "●"
		}
		aliasWidth := contentWidth - 25
		if aliasWidth < 10 {
			aliasWidth = 10
		}
		if aliasWidth > 28 {
			aliasWidth = 28
		}
		line := marker + defaultMark + " " + padDisplay(displayAccountName(row, aliasWidth), aliasWidth) + "  " + padDisplay(localizePlan(row.Plan), 6) + "  " + localizeState(row.State)
		line = fitDisplay(line, contentWidth)
		lines = append(lines, m.styleSelected(line, selected))
	}
	return fillLines(lines, maxRows)
}

func (m model) selectedSummaryLines(maxRows, contentWidth int, includeID bool) []string {
	row, ok := m.selectedRow()
	if !ok {
		return []string{"未选择账号"}
	}
	lines := []string{
		fmt.Sprintf("%s  ·  %s  ·  %s", displayAccountName(row, 28), localizePlan(row.Plan), localizeState(row.State)),
	}
	barWidth := (contentWidth - 14) / 2
	if barWidth < 8 {
		barWidth = 8
	}
	if barWidth > 26 {
		barWidth = 26
	}
	lines = append(lines,
		"5小时  "+m.usageBar(row.FiveH, barWidth),
		"每周   "+m.usageBar(row.Weekly, barWidth),
		fmt.Sprintf("额度   %s    重置额度 %s", sanitizeCell(row.Credits, 18), sanitizeCell(row.Resets, 12)),
	)
	if includeID {
		lines = append(lines, "账号 ID  "+sanitizeCell(row.ProfileID, 56))
	}
	lines = append(lines, "Enter 查看详情   O 直接启动官方 Codex")
	return fillLines(lines, maxRows)
}

func (m model) quotaLines(row Row, width int) []string {
	barWidth := (width - 22) / 2
	if barWidth < 10 {
		barWidth = 10
	}
	if barWidth > 34 {
		barWidth = 34
	}
	lines := []string{
		"5小时剩余  " + m.usageBar(row.FiveH, barWidth),
		"每周剩余   " + m.usageBar(row.Weekly, barWidth),
	}
	for _, window := range row.OtherWindows {
		lines = append(lines, padDisplay(sanitizeCell(window.Label, 10), 10)+"  "+m.usageBar(window.Remaining, barWidth))
	}
	lines = append(lines,
		"额度       "+sanitizeCell(row.Credits, 24),
		"重置额度   "+sanitizeCell(row.Resets, 16),
	)
	return lines
}

func (m model) accountsFooter(width int, empty bool) string {
	if width < 72 {
		if empty {
			return "A 添加   R 整体刷新   Q 退出"
		}
		return "方向键/HJKL 选择  Enter 详情  C 重新登录  R 整体刷新  Q 退出"
	}
	return "↑↓←→/HJKL 选择账号   Enter 详情   O 打开 CLI   C 重新登录   X Desktop 激活   V 恢复指令   R 整体刷新   A 添加   D 取消注册   Q 退出"
}

func (m model) detailFooter(width int) string {
	if width < 72 {
		return "Enter/O 打开   C 重新登录   B/Esc 返回   R 刷新   Q 退出"
	}
	return "Enter/O 启动官方 Codex   C 重新登录   B/Esc 返回账号池   U 设为默认   R 刷新   D 取消注册   Q 退出"
}

func (m model) panel(title string, lines []string, width int) string {
	if width < 20 {
		width = 20
	}
	inner := width - 2
	label := " " + title + " "
	fill := inner - lipgloss.Width(label) - 1
	if fill < 0 {
		fill = 0
	}
	top := "┌─" + label + strings.Repeat("─", fill) + "┐"
	bottom := "└" + strings.Repeat("─", inner) + "┘"
	var b strings.Builder
	b.WriteString(top)
	b.WriteByte('\n')
	for _, line := range lines {
		b.WriteString("│" + fitDisplay(" "+line, inner) + "│")
		b.WriteByte('\n')
	}
	b.WriteString(bottom)
	return b.String()
}

func (m model) viewport() (int, int) {
	width, height := m.width, m.height
	if width <= 0 {
		width = 100
	}
	if height <= 0 {
		height = 30
	}
	if width < 32 {
		width = 32
	}
	return width, height
}

func visibleRange(total, cursor, maxRows int) (int, int) {
	if total <= maxRows {
		return 0, total
	}
	start := cursor - maxRows/2
	if start < 0 {
		start = 0
	}
	if start+maxRows > total {
		start = total - maxRows
	}
	return start, start + maxRows
}

func fillLines(lines []string, count int) []string {
	if count <= 0 {
		return lines
	}
	if len(lines) >= count {
		return append([]string(nil), lines[:count]...)
	}
	out := append([]string(nil), lines...)
	for len(out) < count {
		out = append(out, "")
	}
	return out
}

func fillViewportBelow(content string, width, height int) string {
	if height <= 0 {
		return content
	}
	content = strings.TrimRight(content, "\n")
	lineCount := 0
	if content != "" {
		lineCount = strings.Count(content, "\n") + 1
	}
	if lineCount >= height {
		return content
	}
	if width < 1 {
		width = 1
	}
	blank := strings.Repeat(" ", width)
	var b strings.Builder
	b.WriteString(content)
	for lineCount < height {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(blank)
		lineCount++
	}
	return b.String()
}

func fitDisplay(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return padDisplay(value, width)
	}
	plain := textsafe.Clean(value, 300)
	var b strings.Builder
	for _, r := range plain {
		candidate := b.String() + string(r)
		if lipgloss.Width(candidate)+1 > width {
			break
		}
		b.WriteRune(r)
	}
	return padDisplay(b.String()+"…", width)
}

func wrapDisplay(value string, width int) []string {
	if width < 8 {
		width = 8
	}
	value = textsafe.Clean(value, 500)
	var lines []string
	var current strings.Builder
	for _, r := range value {
		candidate := current.String() + string(r)
		if lipgloss.Width(candidate) > width && current.Len() > 0 {
			lines = append(lines, current.String())
			current.Reset()
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func yesNo(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func (m model) styleSelected(line string, selected bool) string {
	if !selected || m.noColor {
		return line
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86")).Render(line)
}

func (m model) quotaRow(label, value string, barWidth, labelWidth int) string {
	if labelWidth < 1 {
		labelWidth = 1
	}
	label = padDisplay(sanitizeCell(label, labelWidth), labelWidth)
	return label + "  " + m.usageBar(value, barWidth)
}

func (m model) usageBar(value string, width int) string {
	const valueWidth = 6
	pct, ok := parsePercent(value)
	if !ok {
		return strings.Repeat("░", width) + "  " + padDisplay("未提供", valueWidth)
	}
	filled := int((pct/100)*float64(width) + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	if !m.noColor {
		color := lipgloss.Color("42")
		if pct < 50 {
			color = lipgloss.Color("220")
		}
		if pct < 20 {
			color = lipgloss.Color("196")
		}
		bar = lipgloss.NewStyle().Foreground(color).Render(bar)
	}
	return bar + "  " + padDisplay(fmt.Sprintf("%.0f%%", pct), valueWidth)
}

func parsePercent(value string) (float64, bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(value, "%"))
	if trimmed == "" || trimmed == "—" {
		return 0, false
	}
	pct, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || pct < 0 || pct > 100 {
		return 0, false
	}
	return pct, true
}

func localizePlan(value string) string {
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
		return sanitizeCell(value, 9)
	}
}

func localizeState(value string) string {
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
		return "账号状态已变化"
	case "unsupportedruntime":
		return "运行时不支持"
	case "busy":
		return "正忙"
	case "timeout":
		return "超时"
	case "canceled":
		return "已取消"
	case "unknown", "":
		return "未知"
	default:
		return sanitizeCell(value, 14)
	}
}

func boxTitle(title string, width int) string {
	plain := "CodexPool · 账号监控"
	fill := width - lipgloss.Width(plain) - 3
	if fill < 2 {
		fill = 2
	}
	return "╭─ " + title + " " + strings.Repeat("─", fill) + "╮"
}

func boxDivider(label string, width int) string {
	plain := " " + label + " "
	fill := width - lipgloss.Width(plain) - 1
	if fill < 2 {
		fill = 2
	}
	return "├─" + plain + strings.Repeat("─", fill) + "┤"
}

func padDisplay(value string, width int) string {
	current := lipgloss.Width(value)
	if current >= width {
		return value
	}
	return value + strings.Repeat(" ", width-current)
}

func boxBottom(width int) string {
	if width < 4 {
		width = 4
	}
	return "╰" + strings.Repeat("─", width) + "╯"
}

func (m model) selectedRow() (Row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return Row{}, false
	}
	return m.rows[m.cursor], true
}

func (m model) selectedTarget() (ActionTarget, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return ActionTarget{}, false
	}
	row := m.rows[m.cursor]
	if row.ProfileID == "" {
		return ActionTarget{}, false
	}
	return ActionTarget{ProfileID: row.ProfileID, AuthGeneration: row.AuthGeneration, Lifecycle: row.Lifecycle}, true
}

func (m model) selectedProfileID() string {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return ""
	}
	return m.rows[m.cursor].ProfileID
}

func Run(ctx context.Context, rows []Row, refresh RefreshFunc) (Action, error) {
	return RunWithDesktopState(ctx, rows, refresh, DesktopState{})
}

func RunWithDesktopState(ctx context.Context, rows []Row, refresh RefreshFunc, desktop DesktopState) (Action, error) {
	initial := newModelWithDesktop(ctx, rows, refresh, desktop)
	program := tea.NewProgram(initial, tea.WithAltScreen())
	final, err := program.Run()
	if err != nil {
		return Action{}, err
	}
	result, ok := final.(model)
	if !ok {
		return Action{}, fmt.Errorf("unexpected TUI model type %T", final)
	}
	result.cancelRefresh()
	if result.refreshDone != nil {
		<-result.refreshDone
	}
	return result.action, nil
}

func refreshCmd(ctx context.Context, refresh RefreshFunc, seq uint64, done chan<- struct{}) tea.Cmd {
	return func() tea.Msg {
		defer close(done)
		rows, err := refresh(ctx)
		return refreshedMsg{rows: rows, err: err, at: time.Now(), seq: seq}
	}
}

func indexOfProfileID(rows []Row, profileID string) int {
	if profileID == "" {
		return 0
	}
	for i, row := range rows {
		if row.ProfileID == profileID {
			return i
		}
	}
	return -1
}

func sanitizeCell(value string, maxRunes int) string {
	return textsafe.Clean(value, maxRunes)
}
