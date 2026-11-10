"use strict";

const session = document.querySelector('meta[name="codexpool-session"]').content;
const el = id => document.getElementById(id);
let state = { accounts: [], desktop: { kind: "none", label: "无临时切换记录" } };
let selectedId = null;
let currentView = "overview";
let pending = null;
let refreshing = false;
let lastRefresh = 0;
let toastTimer;
let revision = 0;
let query = "";
let focusBeforeConfirm = null;

function node(tag, className, text) {
  const item = document.createElement(tag);
  if (className) item.className = className;
  if (text !== undefined) item.textContent = text;
  return item;
}

function targetOf(account) {
  return { profileId: account.profileId, authGeneration: account.authGeneration, lifecycle: account.lifecycle };
}

async function request(path, payload) {
  const options = payload === undefined ? {} : {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CodexPool-Session": session },
    body: JSON.stringify(payload)
  };
  const response = await fetch(path, options);
  let value;
  try { value = await response.json(); } catch { throw new Error("网页服务没有返回有效数据，请重新打开控制台"); }
  if (!response.ok) throw new Error(value.error || "操作未完成，请重试");
  return value;
}

function showToast(message, isError = false) {
  clearTimeout(toastTimer);
  const toast = el("toast");
  toast.textContent = message;
  toast.classList.toggle("error", isError);
  toast.hidden = false;
  requestAnimationFrame(() => toast.classList.add("open"));
  toastTimer = setTimeout(() => {
    toast.classList.remove("open");
    setTimeout(() => { toast.hidden = true; }, 280);
  }, isError ? 5200 : 3600);
}

function pct(value) {
  return typeof value === "number" && Number.isFinite(value) ? Math.max(0, Math.min(100, value)) : null;
}

function quotaRow(label, remaining, other = false) {
  const row = node("div", "quota-row");
  const title = node("span", "quota-label", label);
  const track = node("span", "quota-track");
  const fill = node("span", "quota-fill" + (other ? " other" : ""));
  const amount = pct(remaining);
  fill.style.width = amount === null ? "0%" : amount + "%";
  track.append(fill);
  const value = node("span", "quota-value" + (amount === null ? " unknown" : ""), amount === null ? "未提供" : Math.round(amount) + "%");
  row.append(title, track, value);
  return row;
}

function stateClass(account) {
  if (account.lifecycle !== "ready" || /异常|未登录|身份|不支持|失败/.test(account.state)) return "error";
  if (/过期|不可用|超时|缓存|忙/.test(account.state)) return "warning";
  return "";
}

function displayName(account) { return account.email || account.alias; }

function hasCache(account) { return Boolean(account.fetchedAt && !account.fetchedAt.startsWith("0001-")); }

function showView(view) {
  currentView = view;
  el("overviewView").hidden = view !== "overview";
  el("detailView").hidden = view !== "detail";
  for (const [id, active] of [["overviewTab", view === "overview"], ["detailTab", view === "detail"]]) {
    const tab = el(id);
    tab.classList.toggle("active", active);
    if (active) tab.setAttribute("aria-current", "page"); else tab.removeAttribute("aria-current");
  }
}

function overviewQuota(remaining) {
  const value = pct(remaining);
  if (value === null) return node("span", "overview-unknown", "未提供");
  const cell = node("span", "overview-quota");
  const track = node("span", "overview-track");
  const fill = node("span", "overview-fill" + (value <= 20 ? " low" : ""));
  fill.style.width = value + "%";
  track.append(fill);
  cell.append(node("strong", "", Math.round(value) + "%"), track);
  return cell;
}

function renderOverview() {
  el("overviewCount").textContent = String(state.accounts.length);
  el("overviewReady").textContent = String(state.accounts.filter(account => account.lifecycle === "ready").length);
  el("overviewCached").textContent = String(state.accounts.filter(hasCache).length);
  el("overviewEmpty").hidden = state.accounts.length !== 0;
  const rows = state.accounts.map(account => {
    const row = node("tr");
    const identity = node("td", "overview-identity");
    const name = node("button", "overview-name", displayName(account));
    name.type = "button";
    name.addEventListener("click", () => { selectAccount(account.profileId); showView("detail"); });
    identity.append(name);
    if (account.active) identity.append(node("span", "overview-default", "默认"));
    if (account.alias !== displayName(account)) identity.append(node("span", "overview-alias", account.alias));
    const status = node("td");
    status.append(node("span", "status-tag " + stateClass(account), account.state || "未知"), node("span", "overview-plan", account.plan || "未知套餐"));
    const five = node("td"); five.append(overviewQuota(account.fiveHourLeft));
    const weekly = node("td"); weekly.append(overviewQuota(account.weeklyLeft));
    const other = node("td", "overview-other");
    const windows = account.otherWindows || [];
    if (windows.length) windows.forEach(window => {
      const line = node("span", "overview-window");
      line.append(node("span", "", window.label || "其它"), overviewQuota(window.remainingPercent));
      other.append(line);
    });
    else other.append(node("span", "overview-unknown", "未提供"));
    const credits = node("td", "overview-credits");
    credits.append(node("strong", "", account.credits || "—"), node("span", "", "重置 " + (account.resetCredits || "—")));
    const go = node("td", "overview-go");
    const detail = node("button", "button button-quiet", "查看");
    detail.type = "button";
    detail.setAttribute("aria-label", "查看 " + displayName(account) + " 的详情");
    detail.addEventListener("click", () => { selectAccount(account.profileId); showView("detail"); });
    go.append(detail);
    row.append(identity, status, five, weekly, other, credits, go);
    return row;
  });
  el("overviewRows").replaceChildren(...rows);
}

function renderRow(account) {
  const row = node("button", "account-row" + (account.profileId === selectedId ? " selected" : ""));
  row.type = "button";
  row.dataset.profileId = account.profileId;
  row.setAttribute("role", "option");
  row.setAttribute("aria-selected", String(account.profileId === selectedId));
  row.setAttribute("aria-label", "查看账号 " + displayName(account));
  const name = displayName(account);
  const main = node("span", "account-main");
  main.append(node("span", "account-name", name), node("span", "account-meta", (account.plan || "未知套餐") + " · " + (account.state || "未知状态")));
  const side = node("span", "account-side");
  const quota = pct(account.fiveHourLeft) ?? pct(account.weeklyLeft) ?? (account.otherWindows || []).map(item => pct(item.remainingPercent)).find(value => value !== null);
  if (quota !== undefined && quota !== null) side.append(node("strong", "", Math.round(quota) + "%"));
  if (account.active) side.append(node("span", "default-mini", "默认"));
  row.append(node("span", "account-avatar", name.slice(0, 1).toUpperCase()), main, side);
  row.addEventListener("click", () => selectAccount(account.profileId));
  return row;
}

function render() {
  renderOverview();
  if (!state.accounts.some(item => item.profileId === selectedId)) selectedId = state.accounts[0]?.profileId || null;
  const filtered = state.accounts.filter(item => (displayName(item) + " " + item.alias).toLocaleLowerCase().includes(query));
  el("accountList").replaceChildren(...filtered.map(renderRow));
  el("searchEmpty").hidden = !query || filtered.length !== 0;
  el("emptyState").hidden = state.accounts.length !== 0;
  el("accountCount").textContent = String(state.accounts.length);
  el("readyCount").textContent = state.accounts.filter(item => item.lifecycle === "ready").length + " 就绪";
  el("desktopState").textContent = state.desktop.label || "状态未知";
  el("restoreButton").disabled = state.desktop.kind === "none";
  el("restoreNoLaunchButton").disabled = state.desktop.kind === "none";
  const current = state.accounts.find(item => item.profileId === selectedId);
  if (current) renderDetail(current);
  else el("detailContent").replaceChildren(node("div", "inspector-empty", "选择左侧账号，查看额度与可执行的操作。"));
}

function detailStat(label, value) {
  const row = node("div", "detail-stat-row");
  row.append(node("span", "", label), node("strong", "", value));
  return row;
}

function actionButton(label, action, account, className = "button button-secondary") {
  const button = node("button", className, label);
  button.type = "button";
  button.addEventListener("click", () => confirmAction(action, account));
  return button;
}

function renderDetail(account) {
  const content = el("detailContent");
  const top = node("div", "detail-topline");
  const titleWrap = node("div");
  titleWrap.append(node("p", "detail-overline", "ACCOUNT / 当前选择"));
  const name = node("h2", "detail-name", displayName(account));
  name.id = "detailTitle";
  titleWrap.append(name);
  if (account.alias !== displayName(account)) titleWrap.append(node("p", "detail-alias", "本地名称 · " + account.alias));
  top.append(titleWrap, node("span", "detail-plan", account.plan || "未知套餐"));
  const tags = node("div", "detail-tags");
  tags.append(node("span", "status-tag " + stateClass(account), account.state || "未知"));
  if (account.active) tags.append(node("span", "active-tag", "当前默认"));
  const quota = node("section", "section");
  const quotaHeading = node("div", "section-heading");
  quotaHeading.append(node("h3", "", "剩余额度"), node("span", "section-note", account.fetchedAt && !account.fetchedAt.startsWith("0001-") ? "更新于 " + new Date(account.fetchedAt).toLocaleString("zh-CN") : "暂无更新时间"));
  const quotaList = node("div", "quota-list");
  quotaList.append(quotaRow("5 小时", account.fiveHourLeft), quotaRow("每周", account.weeklyLeft));
  (account.otherWindows || []).forEach(item => quotaList.append(quotaRow(item.label || "其它", item.remainingPercent, true)));
  quota.append(quotaHeading, quotaList);
  const metrics = node("div", "metric-strip");
  for (const [label, value] of [["Credits", account.credits || "—"], ["Reset Credits", account.resetCredits || "—"]]) {
    const metric = node("div", "metric"); metric.append(node("span", "", label), node("strong", "", value)); metrics.append(metric);
  }
  const actions = node("section", "section");
  const actionHeading = node("div", "section-heading"); actionHeading.append(node("h3", "", "操作")); actions.append(actionHeading);
  const line = node("div", "action-line");
  const runButton = actionButton("打开 Codex CLI", "run", account, "button button-primary");
  const useButton = actionButton("设为默认", "use", account);
  const desktopButton = actionButton("Desktop 激活", "desktopActivate", account);
  runButton.disabled = account.lifecycle !== "ready";
  useButton.disabled = account.lifecycle !== "ready" || account.active;
  desktopButton.disabled = account.lifecycle !== "ready" || state.desktop.kind === "recoveryRequired";
  line.append(runButton, useButton, desktopButton);
  actions.append(line, node("p", "action-caption", "CLI 与 Desktop 操作会交给终端执行；登录和安全确认仍由官方流程处理。"));
  const advanced = node("details", "advanced");
  advanced.append(node("summary", "", "更多账号操作"));
  const more = node("div", "advanced-actions");
  more.append(actionButton("重新登录", "relogin", account), actionButton("修改名称", "rename", account), actionButton("退出登录", "logout", account), actionButton("取消注册", "unregister", account), actionButton("永久删除", "delete", account, "button button-danger"));
  advanced.append(more); actions.append(advanced);
  content.replaceChildren(top, tags, quota, metrics, actions, node("p", "profile-id", "Profile ID · " + account.profileId));
}

function reveal(backdrop, panel) {
  backdrop.hidden = false;
  panel.hidden = false;
  panel.setAttribute("aria-hidden", "false");
  requestAnimationFrame(() => { backdrop.classList.add("open"); panel.classList.add("open"); });
}
function dismiss(backdrop, panel) {
  backdrop.classList.remove("open"); panel.classList.remove("open");
  panel.setAttribute("aria-hidden", "true");
  setTimeout(() => { if (!panel.classList.contains("open")) { backdrop.hidden = true; panel.hidden = true; } }, 390);
}
function selectAccount(id) {
  const account = state.accounts.find(item => item.profileId === id);
  if (!account) return;
  selectedId = id;
  renderDetail(account);
  document.querySelectorAll(".account-row").forEach(row => { const selected = row.dataset.profileId === id; row.classList.toggle("selected", selected); row.setAttribute("aria-selected", String(selected)); });
}

const actionCopy = {
  add: ["添加账号", "将在终端创建独立 Profile，并启动官方 Codex 登录。登录完成后按验证的邮箱命名。"],
  run: ["打开官方 Codex CLI", "将使用此账号的独立 CODEX_HOME，在终端启动官方 Codex CLI。"],
  use: ["设为默认账号", "仅更改 CodexPool 的默认账号；不会切换 ChatGPT Desktop 登录。"],
  desktopActivate: ["激活 Desktop 测试账号", "终端会等待你按 Cmd+Q 完整退出 ChatGPT Desktop，然后进入独立 helper 流程。完成后请核对身份并恢复原登录。"],
  desktopRestore: ["恢复原始 Desktop 登录", "终端会等待你按 Cmd+Q 完整退出 ChatGPT Desktop，再恢复第一次切换前的本地状态。"],
  desktopRestoreNoLaunch: ["仅恢复原始登录", "终端会等待你按 Cmd+Q 完整退出 Desktop，恢复第一次切换前的本地状态，但不会自动打开 ChatGPT。"],
  relogin: ["重新登录", "将在终端启动官方 Codex 登录，并按现有身份与 generation 校验更新 Profile。"],
  rename: ["修改本地名称", "只修改 CodexPool 中显示的名称，不移动账号目录。"],
  logout: ["退出此账号", "将在终端运行官方 Codex logout；命令会再次向你确认。"],
  unregister: ["取消注册", "将在终端从账号池移除该 Profile，保留本地账号数据；命令会再次确认。"],
  delete: ["永久删除账号", "将在终端执行官方 logout 和安全 purge；命令会再次确认。此操作无法轻易撤销。"],
  doctor: ["运行本机诊断", "将在终端显示平台、运行时和项目安全检查。"]
};

function confirmAction(action, account = null) {
  const copy = actionCopy[action];
  if (!copy) return;
  focusBeforeConfirm = document.activeElement;
  pending = { action, account: account ? { ...account } : null };
  el("confirmTitle").textContent = copy[0];
  el("confirmDescription").textContent = account ? displayName(account) + " · " + copy[1] : copy[1];
  el("confirmInputWrap").hidden = action !== "rename";
  el("confirmInput").value = action === "rename" && account ? account.alias : "";
  el("acceptConfirm").textContent = action === "use" ? "设为默认" : "继续";
  reveal(el("confirmBackdrop"), el("confirmDialog"));
  if (action === "rename") el("confirmInput").focus(); else el("cancelConfirm").focus();
}
function closeConfirm() {
  pending = null;
  dismiss(el("confirmBackdrop"), el("confirmDialog"));
  if (focusBeforeConfirm && focusBeforeConfirm.isConnected) focusBeforeConfirm.focus();
  focusBeforeConfirm = null;
}

async function acceptAction() {
  if (!pending) return;
  const { action, account } = pending;
  const body = { action };
  if (account) body.target = targetOf(account);
  if (action === "rename") {
    body.newAlias = el("confirmInput").value.trim();
    if (!body.newAlias) { showToast("请输入新的账号名称", true); return; }
  }
  const button = el("acceptConfirm");
  button.disabled = true;
  const original = button.textContent;
  button.textContent = "处理中…";
  try {
    if (action === "use") {
      state = await request("/api/use", body);
      revision++;
      render();
      showToast("默认账号已更新；Desktop 登录没有变化");
    } else {
      const result = await request("/api/terminal", body);
      if (result.command) showCommandFallback(result.command);
      showToast(result.message || "已打开终端");
    }
    closeConfirm();
  } catch (error) {
    showToast(error.message, true);
  } finally {
    button.disabled = false;
    button.textContent = original;
  }
}

function showCommandFallback(command) {
  const dialog = document.createElement("div");
  dialog.className = "command-fallback";
  dialog.setAttribute("role", "dialog");
  dialog.setAttribute("aria-label", "手动执行命令");
  dialog.append(node("h3", "", "请在终端中运行"));
  const code = node("code", "", command);
  dialog.append(code);
  const buttons = node("div", "confirm-actions");
  const copy = node("button", "button button-primary", "复制命令");
  copy.type = "button";
  copy.addEventListener("click", async () => { try { await navigator.clipboard.writeText(command); showToast("命令已复制"); } catch { showToast("复制失败，请手动选中命令", true); } });
  const close = node("button", "button button-secondary", "关闭");
  close.type = "button";
  close.addEventListener("click", () => dialog.remove());
  buttons.append(close, copy);
  dialog.append(buttons);
  document.body.append(dialog);
  copy.focus();
}

async function refresh(manual = false) {
  if (refreshing) { if (manual) showToast("正在刷新，请稍候"); return; }
  refreshing = true;
  const button = el("refreshButton");
  button.disabled = true;
  button.classList.add("is-refreshing");
  el("refreshNote").textContent = "正在刷新全部账号…";
  if (manual) showToast("已收到刷新请求，正在检查账号额度");
  const startedAtRevision = revision;
  try {
    const result = await request("/api/refresh", {});
    state = startedAtRevision === revision ? result : await request("/api/state");
    lastRefresh = Date.now();
    render();
    const stamp = new Date().toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
    el("refreshNote").textContent = "更新于 " + stamp;
    if (manual) showToast("刷新完成 · " + stamp);
  } catch (error) {
    el("refreshNote").textContent = "刷新未完成";
    showToast(error.message, true);
  } finally {
    refreshing = false;
    button.disabled = false;
    button.classList.remove("is-refreshing");
  }
}

async function start() {
  el("overviewTab").addEventListener("click", () => showView("overview"));
  el("detailTab").addEventListener("click", () => showView("detail"));
  el("addButton").addEventListener("click", () => confirmAction("add"));
  el("doctorButton").addEventListener("click", () => confirmAction("doctor"));
  el("emptyAddButton").addEventListener("click", () => confirmAction("add"));
  el("refreshButton").addEventListener("click", () => refresh(true));
  el("restoreButton").addEventListener("click", () => confirmAction("desktopRestore"));
  el("restoreNoLaunchButton").addEventListener("click", () => confirmAction("desktopRestoreNoLaunch"));
  el("searchInput").addEventListener("input", event => { query = event.target.value.trim().toLocaleLowerCase(); render(); });
  el("accountList").addEventListener("keydown", event => {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    const rows = [...el("accountList").querySelectorAll(".account-row")];
    const index = rows.indexOf(document.activeElement);
    const next = rows[Math.max(0, Math.min(rows.length - 1, index + (event.key === "ArrowDown" ? 1 : -1)))];
    if (next) { event.preventDefault(); next.focus(); selectAccount(next.dataset.profileId); }
  });
  el("cancelConfirm").addEventListener("click", closeConfirm);
  el("confirmBackdrop").addEventListener("click", closeConfirm);
  el("acceptConfirm").addEventListener("click", acceptAction);
  document.addEventListener("keydown", event => {
    if (event.key === "Escape" && pending) closeConfirm();
    if (event.key === "Tab" && pending) {
      const items = [...el("confirmDialog").querySelectorAll("button:not(:disabled), input")].filter(item => item.getClientRects().length > 0);
      if (!items.length) return;
      if (event.shiftKey && document.activeElement === items[0]) { event.preventDefault(); items[items.length - 1].focus(); }
      else if (!event.shiftKey && document.activeElement === items[items.length - 1]) { event.preventDefault(); items[0].focus(); }
    }
  });
  try {
    state = await request("/api/state");
    render();
    el("refreshNote").textContent = "本地缓存已载入";
    await refresh(false);
  } catch (error) {
    el("refreshNote").textContent = "连接失败";
    showToast(error.message, true);
  }
  setInterval(() => { if (!document.hidden) refresh(false); }, 60000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden && Date.now() - lastRefresh > 60000) refresh(false); });
}

start();
