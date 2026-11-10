package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codexpool/internal/diagnostics"
	"codexpool/internal/profile"
	"codexpool/internal/usage"
)

func TestRunStaticHandlesHelpAndVersionWithoutRuntimeConstruction(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"version"}, want: "codexpool " + Version},
		{args: []string{"--help"}, want: "用法："},
		{args: []string{"help", "ignored-extra"}, want: "安全说明："},
		{args: []string{"help"}, want: "Desktop helper 与日常命令/TUI 完全分离"},
	} {
		var out bytes.Buffer
		code, handled := RunStatic(tc.args, &out)
		if !handled || code != ExitOK {
			t.Fatalf("static command %#v was not handled successfully: handled=%t code=%d", tc.args, handled, code)
		}
		if !bytes.Contains(out.Bytes(), []byte(tc.want)) {
			t.Fatalf("static command %#v output missing %q: %q", tc.args, tc.want, out.String())
		}
	}
	if _, handled := RunStatic([]string{"status"}, &bytes.Buffer{}); handled {
		t.Fatal("non-static command must still require full runtime construction")
	}
}

func TestHelpUsesProjectLocalLauncher(t *testing.T) {
	var out bytes.Buffer
	code, handled := RunStatic([]string{"--help"}, &out)
	if !handled || code != ExitOK {
		t.Fatalf("help was not handled successfully: handled=%t code=%d", handled, code)
	}
	if !bytes.Contains(out.Bytes(), []byte("./codexpool.sh")) {
		t.Fatalf("help must advertise the project-local launcher: %q", out.String())
	}
}

func TestNewDefaultKeepsLocalCommandsAvailableWhenBinaryDiscoveryFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool-not-created")
	t.Setenv("CODEXPOOL_HOME", root)
	discoveryErr := errors.New("synthetic Codex discovery failure")
	app, err := newDefault(func() (string, error) { return "", discoveryErr })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructing an unavailable runtime must not create persistent state: %v", err)
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	app.Stdout = &out
	app.Stderr = &stderr
	if code := app.Run(t.Context(), []string{"list"}); code != ExitOK {
		t.Fatalf("local list command must remain available without Codex binary, exit=%d stderr=%q", code, stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := app.Run(t.Context(), []string{"doctor"}); code != ExitUnsupported {
		t.Fatalf("doctor must report missing runtime instead of failing construction, exit=%d", code)
	}
	if !bytes.Contains(out.Bytes(), []byte("codex-binary")) || !bytes.Contains(out.Bytes(), []byte("FAIL")) {
		t.Fatalf("doctor did not surface binary failure: %q", out.String())
	}
}

func TestNewDefaultDoesNotCreateStateForHelpOrVersion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool-not-created")
	t.Setenv("CODEXPOOL_HOME", root)
	t.Setenv("CODEXPOOL_CODEX_BIN", filepath.Join(t.TempDir(), "codex-not-executed"))

	app, err := NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructing the CLI must not create persistent state: %v", err)
	}
	if code := app.Run(t.Context(), []string{"version"}); code != ExitOK {
		t.Fatalf("version returned exit code %d", code)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("version must not create persistent state: %v", err)
	}
}

func TestSplitDoubleDashPreservesForwardedCodexArgs(t *testing.T) {
	before, forwarded := splitDoubleDash([]string{"account-a", "--", "exec", "hello", "--flag"})
	if len(before) != 1 || before[0] != "account-a" {
		t.Fatalf("unexpected pre-separator args: %#v", before)
	}
	if len(forwarded) != 3 || forwarded[0] != "exec" || forwarded[2] != "--flag" {
		t.Fatalf("unexpected forwarded args: %#v", forwarded)
	}
}

func TestPreflightExecRepositoryExplainsGitRequirementWithoutBypass(t *testing.T) {
	cwd := t.TempDir()
	err := preflightExecRepositoryAt([]string{"exec", "hello"}, cwd)
	if err == nil {
		t.Fatal("exec outside a Git repository must receive a local explanation")
	}
	for _, want := range []string{"Git metadata", "--skip-git-repo-check", "-C <Git仓库路径>"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("preflight error missing %q: %v", want, err)
		}
	}

	repo := filepath.Join(cwd, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := preflightExecRepositoryAt([]string{"-C", "repo", "exec", "hello"}, cwd); err != nil {
		t.Fatalf("explicit Git workdir must be allowed: %v", err)
	}
	if err := preflightExecRepositoryAt([]string{"app-server"}, cwd); err != nil {
		t.Fatalf("only exec should receive the Git preflight: %v", err)
	}
}

func TestFriendlyTopLevelAliasesPreserveSafeCanonicalSemantics(t *testing.T) {
	cases := []struct {
		command string
		args    []string
		want    string
		wantArg []string
	}{
		{command: "accounts", want: "list"},
		{command: "ls", args: []string{"--json"}, want: "list", wantArg: []string{"--json"}},
		{command: "show", args: []string{"main"}, want: "status", wantArg: []string{"main"}},
		{command: "info", want: "status"},
		{command: "switch", args: []string{"main"}, want: "use", wantArg: []string{"main"}},
		{command: "open", args: []string{"main", "--", "exec", "hello"}, want: "run", wantArg: []string{"main", "--", "exec", "hello"}},
		{command: "delete", args: []string{"old"}, want: "remove", wantArg: []string{"old", "--purge"}},
	}
	for _, tc := range cases {
		got, gotArgs := normalizeTopLevelCommand(tc.command, tc.args)
		if got != tc.want {
			t.Fatalf("%s normalized to %q, want %q", tc.command, got, tc.want)
		}
		if len(gotArgs) != len(tc.wantArg) {
			t.Fatalf("%s args=%#v want %#v", tc.command, gotArgs, tc.wantArg)
		}
		for i := range gotArgs {
			if gotArgs[i] != tc.wantArg[i] {
				t.Fatalf("%s args=%#v want %#v", tc.command, gotArgs, tc.wantArg)
			}
		}
	}
}

func TestAddWithoutNameCanCancelBeforeAnyLoginStarts(t *testing.T) {
	app := &App{
		Stdin:  bytes.NewBufferString("\n"),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}
	if code := app.commandAdd(t.Context(), nil); code != ExitOK {
		t.Fatalf("blank interactive account name should cancel safely, got %d", code)
	}
}

func TestParseAddArgsDefaultsToFileStoreAndAllowsAutomaticAlias(t *testing.T) {
	autoAlias, autoStore, autoDevice, autoYes, err := parseAddArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if autoAlias != "" || autoStore != profile.StoreFile || autoDevice || autoYes {
		t.Fatalf("unexpected automatic add defaults: alias=%q store=%q device=%t yes=%t", autoAlias, autoStore, autoDevice, autoYes)
	}

	alias, store, device, yes, err := parseAddArgs([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if alias != "alpha" || store != profile.StoreFile || device || yes {
		t.Fatalf("unexpected defaults: alias=%q store=%q device=%t yes=%t", alias, store, device, yes)
	}

	if _, _, _, _, err = parseAddArgs([]string{"alpha", "--store", "keyring", "--device-auth", "--yes"}); err == nil {
		t.Fatal("keyring store must remain disabled until platform-specific isolation tests are validated")
	}
	if _, _, _, _, err := parseAddArgs([]string{"a", "b"}); err == nil {
		t.Fatal("multiple aliases must be rejected")
	}
	if _, _, _, _, err := parseAddArgs([]string{"a", "--store", "auto"}); err == nil {
		t.Fatal("unsupported auto credential store must be rejected")
	}
}

func TestRenameToVerifiedEmailUsesEmailWithoutChangingProfileID(t *testing.T) {
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("pending-test", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	p.Identity.Email = "small-02@example.com"
	app := &App{Store: store}
	renamed, err := app.renameToVerifiedEmail(p)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != p.ID || renamed.Alias != p.Identity.Email {
		t.Fatalf("automatic identity rename=%#v, want same ID and email alias", renamed)
	}
}

func TestParseStatusArgsDoesNotMixAliasAndAll(t *testing.T) {
	opts, err := parseStatusArgs([]string{"--all", "--json", "--cached"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.all || !opts.jsonMode || !opts.cached || opts.alias != "" {
		t.Fatalf("unexpected status options: %#v", opts)
	}
	if _, err := parseStatusArgs([]string{"alpha", "--all"}); err == nil {
		t.Fatal("alias + --all must be rejected")
	}
}

func TestStatusExitCodeForSingleProfile(t *testing.T) {
	cases := []struct {
		name string
		view statusView
		want int
	}{
		{
			name: "ready fresh",
			view: statusView{Lifecycle: profile.LifecycleReady},
			want: ExitOK,
		},
		{
			name: "pending",
			view: statusView{Lifecycle: profile.LifecyclePending},
			want: ExitNotLoggedIn,
		},
		{
			name: "reauth",
			view: statusView{Lifecycle: profile.LifecycleReauthRequired},
			want: ExitNotLoggedIn,
		},
		{
			name: "identity changed",
			view: statusView{Lifecycle: profile.LifecycleReady, ErrorCode: "identityChanged"},
			want: ExitNotLoggedIn,
		},
		{
			name: "busy without cache",
			view: statusView{Lifecycle: profile.LifecycleReady, ErrorCode: "busy"},
			want: ExitBusy,
		},
		{
			name: "unsupported runtime",
			view: statusView{Lifecycle: profile.LifecycleReady, ErrorCode: "unsupportedRuntime"},
			want: ExitUnsupported,
		},
		{
			name: "rpc failure",
			view: statusView{Lifecycle: profile.LifecycleReady, ErrorCode: "unavailable"},
			want: ExitRuntimeFailure,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusExitCode([]statusView{tc.view}, false); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
	if got := statusExitCode([]statusView{{ErrorCode: "unavailable"}, {ErrorCode: "busy"}}, true); got != ExitOK {
		t.Fatalf("--all should render mixed states instead of failing the whole table: %d", got)
	}
}

func TestPercentTextKeepsUnknownDistinctFromZero(t *testing.T) {
	if got := percentText(nil); got != "—" {
		t.Fatalf("unknown percentage should be unavailable, got %q", got)
	}
	zero := 0.0
	if got := percentText(&zero); got != "0%" {
		t.Fatalf("known zero percentage should be 0%%, got %q", got)
	}
}

func TestViewFromCacheShowsCreditsAndNonstandardWindowWhenStandardWindowsAreUnknown(t *testing.T) {
	monthly := int64(43200)
	balance := "1000"
	record := usage.CacheRecord{
		Status: usage.CacheFresh,
		Snapshot: &usage.Snapshot{Buckets: map[string]usage.Bucket{
			"codex": {
				SourceKey: "codex",
				Primary: &usage.Window{
					UsedPercent:             1,
					RemainingPercentDerived: 99,
					WindowDurationMins:      &monthly,
				},
				Credits: &usage.CreditsSnapshot{HasCredits: true, Balance: &balance},
			},
		}},
	}
	view := viewFromCache(profile.Profile{ID: "p_test", Alias: "free-test", Lifecycle: profile.LifecycleReady}, "", record)
	if view.Credits != "1000" {
		t.Fatalf("explicit credits must be shown even without comparable 5H/weekly windows, got %q", view.Credits)
	}
	if view.FiveHLeft != nil || view.WeeklyLeft != nil {
		t.Fatalf("43200-minute window must not be guessed as 5H/weekly: five=%v weekly=%v", view.FiveHLeft, view.WeeklyLeft)
	}
	if len(view.OtherWindows) != 1 || view.OtherWindows[0].Label != "30天" || view.OtherWindows[0].Remaining != 99 {
		t.Fatalf("nonstandard service window must remain visible, got %#v", view.OtherWindows)
	}
}

func TestViewFromCacheDisplaysUniqueStandardWindowsIndependently(t *testing.T) {
	five := usage.FiveHourMinutes
	record := usage.CacheRecord{
		Status: usage.CacheFresh,
		Snapshot: &usage.Snapshot{Buckets: map[string]usage.Bucket{
			"only-five": {Primary: &usage.Window{WindowDurationMins: &five, RemainingPercentDerived: 77}},
		}},
	}
	view := viewFromCache(profile.Profile{ID: "p_test", Alias: "partial", Lifecycle: profile.LifecycleReady}, "", record)
	if view.FiveHLeft == nil || *view.FiveHLeft != 77 {
		t.Fatalf("a unique 5H window should be displayed even if weekly is absent: %#v", view.FiveHLeft)
	}
	if view.WeeklyLeft != nil {
		t.Fatalf("missing weekly window must remain unavailable: %#v", view.WeeklyLeft)
	}
}

func TestRemoveParserRequiresExplicitPurgeFlag(t *testing.T) {
	alias, purge, yes, err := parseRemoveArgs([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if alias != "alpha" || purge || yes {
		t.Fatalf("plain remove should only unregister: alias=%q purge=%t yes=%t", alias, purge, yes)
	}
	_, purge, yes, err = parseRemoveArgs([]string{"alpha", "--purge", "--yes"})
	if err != nil || !purge || !yes {
		t.Fatalf("explicit purge parse failed: purge=%t yes=%t err=%v", purge, yes, err)
	}
}

func TestDoctorExitCodeIsIndependentOfOutputFormat(t *testing.T) {
	if got := doctorExitCode(diagnostics.Report{Checks: []diagnostics.Check{{Name: "ok", Status: "ok"}}}); got != ExitOK {
		t.Fatalf("all-ok doctor should exit 0, got %d", got)
	}
	if got := doctorExitCode(diagnostics.Report{Checks: []diagnostics.Check{{Name: "runtime-version", Status: "fail"}}}); got != ExitUnsupported {
		t.Fatalf("failed doctor must exit unsupported even when JSON is requested, got %d", got)
	}
}
