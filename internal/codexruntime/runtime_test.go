package codexruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codexpool/internal/profile"
)

func TestDiscoverMacBinary(t *testing.T) {
	root := t.TempDir()
	modern := filepath.Join(root, "modern-codex")
	legacy := filepath.Join(root, "legacy-codex")
	if err := os.WriteFile(legacy, []byte("legacy"), 0700); err != nil {
		t.Fatal(err)
	}
	candidates := []string{modern, legacy}
	if got, err := discoverMacBinary(candidates); err != nil || got != legacy {
		t.Fatalf("legacy fallback: got %q, err %v", got, err)
	}
	if err := os.WriteFile(modern, []byte("modern"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := discoverMacBinary(candidates); err != nil || got != modern {
		t.Fatalf("modern layout: got %q, err %v", got, err)
	}
	if err := os.Chmod(modern, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := discoverMacBinary(candidates); err != nil || got != legacy {
		t.Fatalf("non-executable must be skipped: got %q, err %v", got, err)
	}
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverMacBinary(candidates); err == nil {
		t.Fatal("missing executable must return a discovery error")
	}
	if err := os.Remove(modern); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(modern, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverMacBinary(candidates); err == nil {
		t.Fatal("directories must not be accepted as executables")
	}
}

func TestLoginCompatibilityIsScopedToReviewedVersion(t *testing.T) {
	for _, version := range []string{ReviewedLoginVersion, "0.161.0"} {
		t.Run(version, func(t *testing.T) {
			store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
			if err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(t.TempDir(), "codex")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\necho 'codex-cli "+version+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			rt, err := New(binary, store)
			if err != nil {
				t.Fatal(err)
			}
			rt.ExpectedVersion = "0.154.0-alpha.6.2"
			if _, err := rt.RequireCompatible(context.Background()); !errors.Is(err, ErrUnsupportedRuntime) {
				t.Fatalf("general operations must remain pinned: %v", err)
			}
			_, err = rt.RequireLoginCompatible(context.Background())
			if version == ReviewedLoginVersion && err != nil {
				t.Fatalf("reviewed login must be allowed: %v", err)
			}
			if version != ReviewedLoginVersion && !errors.Is(err, ErrUnsupportedRuntime) {
				t.Fatalf("unreviewed login must remain blocked: %v", err)
			}
		})
	}
}

func TestStrictProfileEnvForcesHomeAndRemovesEndpointOverride(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"CODEX_HOME=/global/codex",
		"OPENAI_BASE_URL=https://override.invalid",
		"SAFE_VALUE=keep",
	}
	got := StrictProfileEnv(parent, "/profiles/p1/codex-home")
	joined := strings.Join(got, "\n")
	for _, forbidden := range []string{"override.invalid", "CODEX_HOME=/global/codex"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("forbidden inherited value remained: %s", forbidden)
		}
	}
	if !strings.Contains(joined, "SAFE_VALUE=keep") {
		t.Fatalf("non-sensitive environment was unexpectedly removed: %q", joined)
	}
	if strings.Count(joined, "CODEX_HOME=") != 1 || !strings.Contains(joined, "CODEX_HOME=/profiles/p1/codex-home") {
		t.Fatalf("profile CODEX_HOME not forced exactly once: %q", joined)
	}
	if parent[1] != "CODEX_HOME=/global/codex" {
		t.Fatal("parent environment slice was mutated")
	}
}

func TestValidateCredentialStoreConfigAllowsOfficialCodexAdditions(t *testing.T) {
	data := []byte(`# Codex may persist normal user settings alongside the store selector.
model = "gpt-5.6-terra"
cli_auth_credentials_store = "file" # CodexPool requires file store for v0.1
model_reasoning_effort = "high"

[projects."/tmp/example"]
trust_level = "trusted"
`)
	if err := validateCredentialStoreConfig(data, profile.StoreFile); err != nil {
		t.Fatalf("official Codex config additions must not invalidate the Profile: %v", err)
	}
}

func TestValidateCredentialStoreConfigAllowsObservedProjectTrustConfig(t *testing.T) {
	// Synthetic example of the project-trust shape written by Codex 0.153.4.
	// The top-level file-store selector must remain valid when Codex appends a
	// project-specific table.
	data := []byte(`cli_auth_credentials_store = "file"

[projects."/tmp/example-project"]
trust_level = "trusted"
`)
	if err := validateCredentialStoreConfig(data, profile.StoreFile); err != nil {
		t.Fatalf("observed official project trust config must remain valid: %v", err)
	}
}

func TestValidateCredentialStoreConfigRejectsWrongMissingNestedOrDuplicateStore(t *testing.T) {
	for name, data := range map[string]string{
		"wrong store":   `cli_auth_credentials_store = "keyring"`,
		"missing store": `model = "gpt-5.6-terra"`,
		"nested only":   "model = \"gpt-5.6-terra\"\n[projects.\"/tmp/example\"]\ncli_auth_credentials_store = \"file\"\n",
		"duplicate":     "cli_auth_credentials_store = \"file\"\ncli_auth_credentials_store = \"file\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateCredentialStoreConfig([]byte(data), profile.StoreFile); err == nil {
				t.Fatalf("unsafe config must fail closed: %q", data)
			}
		})
	}
}

func TestValidateRunArgsBlocksRealRoutingOverridesButAllowsLiteralPromptContent(t *testing.T) {
	for _, args := range [][]string{
		{"login"},
		{"logout"},
		{"-m", "gpt-5", "login"},
		{"-c", `model="x"`},
		{`--config=provider="x"`},
		{"-p", "alternate"},
		{"--profile=alternate"},
		{"--oss"},
		{"--local-provider", "ollama"},
		{"--ignore-user-config"},
		{"--enable", "feature", "login"},
		{"--disable=feature"},
		{"--remote", "ws://127.0.0.1:9000"},
		{"--remote=unix:///tmp/codex.sock"},
		{"--remote-auth-token-env", "TOKEN_ENV"},
	} {
		if err := ValidateRunArgs(args); err == nil {
			t.Fatalf("expected args to be blocked: %#v", args)
		}
	}
	if err := ValidateRunArgs([]string{"exec", "please explain login behavior"}); err != nil {
		t.Fatalf("prompt content should not be treated as an auth subcommand: %v", err)
	}
	if err := ValidateRunArgs([]string{"exec", "--", "--config", "literal prompt text"}); err != nil {
		t.Fatalf("arguments after Codex -- must be treated as literal content: %v", err)
	}
}

func TestResolveCurrentGenerationFailsClosedAfterReloginGenerationChange(t *testing.T) {
	store, _ := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	p, err := store.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := New(filepath.Join(t.TempDir(), "fake-codex"), store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.resolveCurrentGeneration(p); err != nil {
		t.Fatalf("unchanged generation should resolve: %v", err)
	}
	if _, err := store.Update(p.ID, func(current *profile.Profile) error {
		current.AuthGeneration++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.resolveCurrentGeneration(p); err == nil || !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("expected ErrProfileChanged, got %v", err)
	}
}

func TestResolveCurrentGenerationRejectsMutatedHome(t *testing.T) {
	store, _ := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	p, err := store.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	rt, _ := New(filepath.Join(t.TempDir(), "fake-codex"), store)
	forged := p
	forged.CodexHome = filepath.Join(t.TempDir(), "other-home")
	if _, err := rt.resolveCurrentGeneration(forged); err == nil || !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("expected immutable-home ErrProfileChanged, got %v", err)
	}
}

func TestUnavailableRuntimeFailsBeforeCreatingProbeState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool-not-created")
	store, err := profile.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	discoveryErr := errors.New("synthetic Codex discovery failure")
	rt, err := NewUnavailable(store, discoveryErr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Version(context.Background()); err == nil || !errors.Is(err, discoveryErr) {
		t.Fatalf("unavailable runtime must preserve discovery failure, got %v", err)
	}
	if _, err := rt.RequireCompatible(context.Background()); err == nil || !errors.Is(err, discoveryErr) {
		t.Fatalf("compatibility gate must reject an unavailable runtime even without an expected version, got %v", err)
	}
	if _, err := os.Lstat(store.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing runtime probe must not create CodexPool state: %v", err)
	}
}

func TestParseSemanticVersion(t *testing.T) {
	for _, input := range []string{"codex-cli 0.153.4", "Codex 0.153.4 (desktop)", "0.153.4"} {
		got, err := ParseSemanticVersion(input)
		if err != nil || got != "0.153.4" {
			t.Fatalf("parse %q: got %q err=%v", input, got, err)
		}
	}
	if got, err := ParseSemanticVersion("codex-cli 0.154.0-alpha.6.2"); err != nil || got != "0.154.0-alpha.6.2" {
		t.Fatalf("parse prerelease runtime: got %q err=%v", got, err)
	}
	if _, err := ParseSemanticVersion("synthetic-version"); err == nil {
		t.Fatal("unparseable runtime version must fail closed")
	}
}
