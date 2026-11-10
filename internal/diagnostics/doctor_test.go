package diagnostics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"codexpool/internal/codexruntime"
	"codexpool/internal/profile"
)

func TestLoadEvidenceUsesPinnedBuiltInMetadataWhenRepositoryManifestUnavailable(t *testing.T) {
	got, err := loadEvidence("")
	if err != nil {
		t.Fatal(err)
	}
	if got.CodexVersion != "0.154.0-alpha.6.2" || got.ResearchDate != "2026-09-13" {
		t.Fatalf("unexpected built-in evidence: %#v", got)
	}
}

func TestLoadEvidenceRejectsManifestHashMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{"researchDate":"2026-09-07","codexVersion":"0.153.4"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEvidence(path); err == nil {
		t.Fatal("unreviewed evidence manifest must not be accepted")
	}
}

func TestDoctorTreatsDiscoveredEvidenceHashMismatchAsFailure(t *testing.T) {
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	rt, err := codexruntime.New(binary, store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{"researchDate":"2026-09-07","codexVersion":"0.153.4"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), store, rt, manifest, false)
	if !hasFailedCheck(report.Checks, "protocol-evidence") {
		t.Fatalf("a discovered but unpinned evidence manifest must fail doctor: %#v", report.Checks)
	}
}

func TestDoctorReportsUnavailableBinaryWithoutExecutingRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool-not-created")
	store, err := profile.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	discoveryErr := errors.New("synthetic Codex discovery failure")
	rt, err := codexruntime.NewUnavailable(store, discoveryErr)
	if err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), store, rt, "", false)
	if !hasFailedCheck(report.Checks, "codex-binary") {
		t.Fatalf("doctor must surface unavailable binary as failure: %#v", report.Checks)
	}
}

func TestDoctorDoesNotExecuteRuntimeProbeUnlessExplicitlyRequested(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool")
	store, err := profile.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "codex")
	marker := filepath.Join(t.TempDir(), "executed")
	script := "#!/bin/sh\ntouch \"" + marker + "\"\necho synthetic-version\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	rt, err := codexruntime.New(binary, store)
	if err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), store, rt, "", false)
	if report.RuntimeVersion != "" {
		t.Fatalf("runtime version should remain unprobed, got %q", report.RuntimeVersion)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("doctor executed the runtime without --probe-runtime: stat=%v", err)
	}
	foundSkip := false
	for _, check := range report.Checks {
		if check.Name == "runtime-version" && check.Status == "skip" {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatalf("doctor did not report an explicit skipped runtime probe: %#v", report.Checks)
	}
}

func TestDoctorProbeWarnsWhenRuntimeDiffersFromPinnedInteractiveEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pool")
	store, err := profile.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\necho 'codex-cli 0.154.0'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	rt, err := codexruntime.New(binary, store)
	if err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), store, rt, "", true)
	if report.RuntimeVersion != "0.154.0" {
		t.Fatalf("runtime semantic version not recorded: %q", report.RuntimeVersion)
	}
	foundWarn := false
	for _, check := range report.Checks {
		if check.Name == "runtime-version" && check.Status == "warn" {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("runtime/evidence mismatch should warn while read-only queries use strict wire validation: %#v", report.Checks)
	}
}

func TestCheckProfileRejectsNonDirectoryHomeBeforeRuntimeUse(t *testing.T) {
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(p.CodexHome); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.CodexHome, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !hasFailedCheck(checkProfile(store, p), "profile:a:home") {
		t.Fatal("doctor must reject a CODEX_HOME that is not a directory")
	}
}

func TestCheckProfileRejectsNonRegularConfigBeforeRuntimeUse(t *testing.T) {
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(p.CodexHome, "config.toml")
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(configPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if !hasFailedCheck(checkProfile(store, p), "profile:a:config") {
		t.Fatal("doctor must reject a config.toml that is not a regular private file")
	}
}

func hasFailedCheck(checks []Check, name string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == "fail" {
			return true
		}
	}
	return false
}
