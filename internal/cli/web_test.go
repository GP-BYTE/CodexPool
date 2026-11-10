package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codexpool/internal/profile"
	"codexpool/internal/securefs"
	"codexpool/internal/usage"
)

func syntheticWebConsole(t *testing.T) (*webConsole, profile.Profile) {
	t.Helper()
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("synthetic@example.invalid", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	p, err = store.UpdateExpected(p, func(next *profile.Profile) error {
		next.Lifecycle = profile.LifecycleReady
		next.Identity = profile.Identity{Email: "synthetic@example.invalid", PlanType: "free", ObservedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Store: store, Usage: usage.NewService(nil, Version, "synthetic")}
	return &webConsole{app: app, authority: "127.0.0.1:48123", token: "synthetic-session", repoRoot: "/tmp/synthetic-project"}, p
}

func webRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "http://127.0.0.1:48123"+path, strings.NewReader(body))
	r.Host = "127.0.0.1:48123"
	if method == http.MethodPost {
		r.Header.Set("Origin", "http://127.0.0.1:48123")
		r.Header.Set("X-CodexPool-Session", "synthetic-session")
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func TestWebConsoleOnlyServesExactLoopbackHostAndRejectsCrossOriginWrites(t *testing.T) {
	w, _ := syntheticWebConsole(t)
	handler := w.handler()
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, webRequest(http.MethodGet, "/", ""))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "CodexPool · 账号工作台") || !strings.Contains(page.Body.String(), "synthetic-session") {
		t.Fatalf("page status=%d body=%q", page.Code, page.Body.String())
	}
	for _, marker := range []string{`id="overviewTab"`, `id="overviewView"`, `id="overviewRows"`, `id="accountList"`, `id="searchInput"`, `id="detailContent"`, `id="desktopState"`, `id="confirmDialog"`} {
		if !strings.Contains(page.Body.String(), marker) {
			t.Fatalf("workbench missing %s", marker)
		}
	}
	style := httptest.NewRecorder()
	handler.ServeHTTP(style, webRequest(http.MethodGet, "/dashboard.css", ""))
	if style.Code != http.StatusOK || !strings.Contains(style.Body.String(), ".overview-table") {
		t.Fatalf("overview stylesheet status=%d", style.Code)
	}
	if !strings.Contains(page.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
		t.Fatal("web response has no restrictive content security policy")
	}
	for _, altered := range []func(*http.Request){
		func(r *http.Request) { r.Host = "attacker.example:48123" },
		func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") },
		func(r *http.Request) { r.Header.Del("X-CodexPool-Session") },
	} {
		req := webRequest(http.MethodPost, "/api/use", `{}`)
		altered(req)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != http.StatusForbidden {
			t.Fatalf("unsafe request status=%d, want forbidden", out.Code)
		}
	}
}

func TestWebConsoleCachedStateAndImmutableDefaultSelection(t *testing.T) {
	w, p := syntheticWebConsole(t)
	handler := w.handler()
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, webRequest(http.MethodGet, "/api/state", ""))
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), p.ID) || strings.Contains(read.Body.String(), "auth.json") {
		t.Fatalf("cached state status=%d body=%q", read.Code, read.Body.String())
	}
	input := webAction{Action: "use", Target: webTarget{ProfileID: p.ID, AuthGeneration: p.AuthGeneration, Lifecycle: p.Lifecycle}}
	body, _ := json.Marshal(input)
	set := httptest.NewRecorder()
	handler.ServeHTTP(set, webRequest(http.MethodPost, "/api/use", string(body)))
	if set.Code != http.StatusOK || !bytes.Contains(set.Body.Bytes(), []byte(`"active":true`)) {
		t.Fatalf("set default status=%d body=%q", set.Code, set.Body.String())
	}
	input.Target.AuthGeneration++
	body, _ = json.Marshal(input)
	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, webRequest(http.MethodPost, "/api/use", string(body)))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale target status=%d", stale.Code)
	}
}

func TestWebTerminalHandoffUsesImmutableIDAndKeepsOfficialConfirmation(t *testing.T) {
	w, p := syntheticWebConsole(t)
	target := webTarget{ProfileID: p.ID, AuthGeneration: p.AuthGeneration, Lifecycle: p.Lifecycle}
	for _, tc := range []struct{ action, want string }{
		{"run", "./codexpool.sh open '" + p.ID + "'"},
		{"desktopActivate", "codexpool-desktop-helper activate --profile-id '" + p.ID + "'"},
		{"delete", "./codexpool.sh delete '" + p.ID + "'"},
		{"unregister", "./codexpool.sh remove '" + p.ID + "'"},
	} {
		command, err := w.terminalCommand(webAction{Action: tc.action, Target: target})
		if err != nil || !strings.Contains(command, tc.want) || strings.Contains(command, "--yes") {
			t.Fatalf("%s command=%q error=%v", tc.action, command, err)
		}
	}
	if _, err := w.terminalCommand(webAction{Action: "desktopRestore"}); err == nil {
		t.Fatal("Desktop restore without a recorded transaction was offered")
	}
	bad := target
	bad.AuthGeneration++
	if _, err := w.terminalCommand(webAction{Action: "run", Target: bad}); err == nil {
		t.Fatal("stale immutable target was accepted")
	}
	if _, err := w.terminalCommand(webAction{Action: "rename", Target: target, NewAlias: "bad\ncommand"}); err == nil {
		t.Fatal("invalid alias was accepted")
	}
}

func TestWebRestoreOffersNoLaunchWhenSyntheticJournalExists(t *testing.T) {
	w, p := syntheticWebConsole(t)
	root := filepath.Join(w.app.Store.Root, "desktop-activation")
	if err := securefs.EnsurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	journal := map[string]any{"version": 1, "phase": "verified", "profileId": p.ID, "authGeneration": p.AuthGeneration, "lifecycle": string(p.Lifecycle)}
	encoded, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := securefs.AtomicWriteFile(filepath.Join(root, "activation-journal.json"), encoded); err != nil {
		t.Fatal(err)
	}
	command, err := w.terminalCommand(webAction{Action: "desktopRestoreNoLaunch"})
	if err != nil || !strings.Contains(command, "restore --i-understand-this-switches-desktop-auth") || !strings.Contains(command, "--no-launch") {
		t.Fatalf("no-launch restore command=%q error=%v", command, err)
	}
}

func TestWebTerminalRequestRejectsStaleTargetBeforeLauncher(t *testing.T) {
	w, p := syntheticWebConsole(t)
	called := false
	w.launchTerminal = func(context.Context, string) error { called = true; return nil }
	input := webAction{Action: "run", Target: webTarget{ProfileID: p.ID, AuthGeneration: p.AuthGeneration + 1, Lifecycle: p.Lifecycle}}
	body, _ := json.Marshal(input)
	out := httptest.NewRecorder()
	w.handler().ServeHTTP(out, webRequest(http.MethodPost, "/api/terminal", string(body)))
	if out.Code != http.StatusConflict || called {
		t.Fatalf("stale request status=%d terminal launched=%t", out.Code, called)
	}
}

// Opt-in local visual fixture. It only serves synthetic profiles below TempDir.
func TestWebVisualFixture(t *testing.T) {
	if os.Getenv("CODEXPOOL_WEB_VISUAL_FIXTURE") != "1" {
		t.Skip("visual fixture is opt-in")
	}
	w, _ := syntheticWebConsole(t)
	monthly := int64(43200)
	five := int64(300)
	weekly := int64(10080)
	for i := 0; i < 6; i++ {
		alias := []string{"alpha@example.invalid", "beta@example.invalid", "gamma@example.invalid", "delta@example.invalid", "epsilon@example.invalid", "zeta@example.invalid"}[i]
		p, err := w.app.Store.Resolve(alias)
		if err != nil {
			p, err = w.app.Store.CreatePending(alias, profile.StoreFile)
			if err != nil {
				t.Fatal(err)
			}
			p, err = w.app.Store.UpdateExpected(p, func(next *profile.Profile) error {
				next.Lifecycle = profile.LifecycleReady
				next.Identity = profile.Identity{Email: alias, PlanType: "free", ObservedAt: time.Now().UTC()}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		now := time.Now().UTC()
		balance := "1000"
		buckets := map[string]usage.Bucket{"codex": {SourceKey: "codex", Primary: &usage.Window{UsedPercent: float64(i + 4), RemainingPercentDerived: float64(96 - i), WindowDurationMins: &monthly}, Credits: &usage.CreditsSnapshot{HasCredits: true, Balance: &balance}}}
		if i%2 == 1 {
			buckets = map[string]usage.Bucket{"codex": {SourceKey: "codex", Primary: &usage.Window{UsedPercent: float64(i + 14), RemainingPercentDerived: float64(86 - i), WindowDurationMins: &five}, Secondary: &usage.Window{UsedPercent: float64(i + 9), RemainingPercentDerived: float64(91 - i), WindowDurationMins: &weekly}, Credits: &usage.CreditsSnapshot{HasCredits: true, Balance: &balance}}}
		}
		record := usage.CacheRecord{Status: usage.CacheFresh, FetchedAt: now, LastAttemptAt: now, Snapshot: &usage.Snapshot{FetchedAt: now, Buckets: buckets}}
		if err := usage.WriteCache(p, record); err != nil {
			t.Fatal(err)
		}
	}
	handler := w.handler()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/refresh" {
			// The visual fixture has no official runtime; retain its synthetic cache.
			w.serveState(out, r)
			return
		}
		handler.ServeHTTP(out, r)
	}))
	w.authority = server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	t.Logf("synthetic visual fixture: %s", server.URL)
	select {
	case <-time.After(75 * time.Second):
	case <-t.Context().Done():
	}
}
