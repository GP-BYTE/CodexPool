package profile

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codexpool/internal/securefs"
)

func TestNewStorePinsExistingSymlinkAncestorsBeforeInitialization(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	if err := os.Mkdir(first, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "root-link")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	requested := filepath.Join(link, "missing", "pool")
	s, err := NewStore(requested)
	if err != nil {
		t.Fatal(err)
	}
	resolvedFirst, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(resolvedFirst, "missing", "pool")
	if filepath.Clean(s.Root) != filepath.Clean(wantRoot) {
		t.Fatalf("managed root was not pinned to the resolved ancestor: got %q want %q", s.Root, wantRoot)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wantRoot); err != nil {
		t.Fatalf("pinned managed root was not created at the original resolved target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second, "missing", "pool")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replaced symlink redirected managed state: %v", err)
	}
}

func TestInitRefusesToAdoptUnrelatedExistingDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("user-data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err == nil {
		t.Fatal("existing unrelated directory must not be silently adopted as CODEXPOOL_HOME")
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("refused unrelated directory was chmodded: %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(keep)
	if err != nil || string(data) != "user-data\n" {
		t.Fatalf("refused unrelated directory contents changed: data=%q err=%v", data, err)
	}
}

func TestInitMigratesRecognizableLegacyManagedRootWithMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "legacy")
	for _, dir := range []string{root, filepath.Join(root, "accounts"), filepath.Join(root, "locks")} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("recognizable legacy root should be migrated safely: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(root, managedRootMarker))
	if err != nil || string(marker) != managedRootMarkerContents {
		t.Fatalf("legacy root marker not created correctly: %q err=%v", marker, err)
	}
}

func TestCreatePendingUsesImmutableRandomDirectoryAndActiveAlias(t *testing.T) {
	root := t.TempDir()
	s, err := NewStore(filepath.Join(root, "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreatePending("account-a", StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.ID == p.Alias {
		t.Fatalf("expected opaque immutable profile id, got %q", p.ID)
	}
	if filepath.Base(filepath.Dir(p.CodexHome)) != p.ID {
		t.Fatalf("codex home not namespaced by profile id: %s", p.CodexHome)
	}
	if _, err := os.Stat(p.CodexHome); err != nil {
		t.Fatalf("codex home missing: %v", err)
	}
	p, err = s.Update(p.ID, func(current *Profile) error {
		current.Lifecycle = LifecycleReady
		current.Identity = Identity{PlanType: "plus", ObservedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetActive("account-a"); err != nil {
		t.Fatal(err)
	}
	active, err := s.Active()
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != p.ID {
		t.Fatalf("active profile mismatch: %s != %s", active.ID, p.ID)
	}
}

func TestAliasValidationRejectsTraversalAndControlCharacters(t *testing.T) {
	for _, alias := range []string{"../x", "a/b", `a\b`, "", "x\nboom", "x\x1b[31m"} {
		if err := ValidateAlias(alias); err == nil {
			t.Fatalf("expected alias %q to be rejected", alias)
		}
	}
}

func TestAliasValidationAllowsVerifiedEmailHandle(t *testing.T) {
	if err := ValidateAlias("small-02+pool@example.com"); err != nil {
		t.Fatalf("verified email must be a safe user-facing alias: %v", err)
	}
}

func TestRegistryFileIsPrivate(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	p, err := s.CreatePending("a", StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(s.Root, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("registry permissions too broad: %o", info.Mode().Perm())
	}
	configInfo, err := os.Stat(filepath.Join(p.CodexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if configInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("profile config permissions too broad: %o", configInfo.Mode().Perm())
	}
}

func TestPendingProfileCannotBecomeDefault(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	if _, err := s.CreatePending("pending", StoreFile); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetActive("pending"); err == nil || !errors.Is(err, ErrNotReady) {
		t.Fatalf("expected ErrNotReady, got %v", err)
	}
}

func TestSetActiveExpectedRejectsStaleGeneration(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	p, err := s.CreatePending("a", StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.UpdateExpected(p, func(current *Profile) error {
		current.Lifecycle = LifecycleReady
		current.Identity = Identity{PlanType: "plus", ObservedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := p
	if _, err := s.UpdateExpected(p, func(current *Profile) error {
		current.AuthGeneration++
		current.Lifecycle = LifecycleReauthRequired
		current.Identity = Identity{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetActiveExpected(stale); err == nil || !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("stale displayed target must not become active, got %v", err)
	}
	if _, err := s.Active(); err == nil {
		t.Fatal("stale SetActiveExpected unexpectedly created an active profile")
	}
}

func TestUncertainRegistryCommitNeverDeletesReferencedProfileData(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	p, err := s.CreatePending("a", StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(p.CodexHome)
	sentinel := errors.New("synthetic post-rename durability failure")
	retained, err := s.handleCreateFailure(p, root, &securefs.AtomicWriteError{State: securefs.CommitDurabilityUncertain, Err: sentinel})
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("uncertain commit should remain an explicit recoverable error: %v", err)
	}
	if retained.ID != p.ID {
		t.Fatalf("referenced profile must be returned for recovery: got %#v want %s", retained, p.ID)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("registry-referenced Profile data was deleted after uncertain commit: %v", err)
	}
	resolved, err := s.Resolve(p.ID)
	if err != nil || resolved.ID != p.ID {
		t.Fatalf("registry reference was lost during uncertain-create recovery: %#v err=%v", resolved, err)
	}
}

func TestDuplicateAliasDoesNotLeakAnotherProfileDirectory(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	if _, err := s.CreatePending("a", StoreFile); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(s.AccountsDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePending("A", StoreFile); err == nil {
		t.Fatal("case-insensitive duplicate alias must be rejected")
	}
	after, err := os.ReadDir(s.AccountsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("duplicate add leaked a profile directory: before=%d after=%d", len(before), len(after))
	}
}

func TestRenameDoesNotMoveImmutableProfileDirectory(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	p, err := s.CreatePending("old", StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	oldHome := p.CodexHome
	p, err = s.Rename(p.ID, "new")
	if err != nil {
		t.Fatal(err)
	}
	if p.CodexHome != oldHome {
		t.Fatalf("rename changed immutable CODEX_HOME: %q -> %q", oldHome, p.CodexHome)
	}
}

func TestPurgeDataDoesNotDeletePersistentProfileLockFile(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	p, err := s.CreatePending("a", StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	lockPath, err := s.OperationLockPath(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := securefs.TryLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeData(p); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("profile purge must not unlink persistent lock file: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("profile purge replaced persistent lock inode")
	}
}

func TestPurgeDataRejectsForgedProfileHome(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	p, err := s.CreatePending("a", StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	p.CodexHome = filepath.Join(t.TempDir(), "outside")
	if err := s.PurgeData(p); err == nil {
		t.Fatal("purge must reject a profile whose CODEX_HOME is not its immutable managed path")
	}
}

func TestLoadRejectsMalformedRegistryInvariants(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Registry, Profile)
	}{
		{
			name: "duplicate id",
			mutate: func(r *Registry, p Profile) {
				copyP := p
				copyP.Alias = "b"
				r.Profiles = append(r.Profiles, copyP)
			},
		},
		{
			name: "duplicate alias",
			mutate: func(r *Registry, p Profile) {
				copyP := p
				copyP.ID = "p_11111111111111111111111111111111"
				copyP.Alias = "A"
				copyP.CodexHome = filepath.Join(filepath.Dir(filepath.Dir(p.CodexHome)), copyP.ID, "codex-home")
				r.Profiles = append(r.Profiles, copyP)
			},
		},
		{
			name: "forged home",
			mutate: func(r *Registry, p Profile) {
				r.Profiles[0].CodexHome = filepath.Join(filepath.Dir(p.CodexHome), "other-home")
			},
		},
		{
			name: "dangling active",
			mutate: func(r *Registry, p Profile) {
				r.ActiveProfileID = "p_22222222222222222222222222222222"
			},
		},
		{
			name: "zero createdAt",
			mutate: func(r *Registry, p Profile) {
				r.Profiles[0].CreatedAt = time.Time{}
			},
		},
		{
			name: "ready without verified identity",
			mutate: func(r *Registry, p Profile) {
				r.Profiles[0].Lifecycle = LifecycleReady
				r.Profiles[0].Identity = Identity{}
			},
		},
		{
			name: "active pending",
			mutate: func(r *Registry, p Profile) {
				r.ActiveProfileID = p.ID
			},
		},
		{
			name: "invalid id",
			mutate: func(r *Registry, p Profile) {
				r.Profiles[0].ID = "../escape"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
			p, err := s.CreatePending("a", StoreFile)
			if err != nil {
				t.Fatal(err)
			}
			r, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&r, p)
			data, err := json.MarshalIndent(r, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.RegistryPath(), append(data, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(); err == nil {
				t.Fatal("malformed registry must fail closed")
			}
		})
	}
}

func TestLoadRejectsUnknownRegistryFieldsAndTrailingJSON(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	if _, err := s.CreatePending("a", StoreFile); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	object["unexpected"] = true
	unknown, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.RegistryPath(), append(unknown, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("unknown registry fields must fail closed instead of being silently discarded")
	}

	delete(object, "unexpected")
	valid, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	multiple := append(append(valid, '\n'), []byte(`{"extra":true}`)...)
	if err := os.WriteFile(s.RegistryPath(), append(multiple, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("multiple JSON values in the registry must fail closed")
	}
}

func TestLoadRejectsDuplicateRegistryJSONKeys(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"schemaVersion":1,"revision":1,"revision":2,"profiles":[]}`)
	if err := os.WriteFile(s.RegistryPath(), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("duplicate registry JSON keys must fail closed")
	}
}

func TestRegistryRevisionCannotWrap(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	if _, err := s.CreatePending("a", StoreFile); err != nil {
		t.Fatal(err)
	}
	r, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	r.Revision = math.MaxUint64
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.RegistryPath(), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.WithRegistry(func(*Registry) error { return nil }); err == nil {
		t.Fatal("registry revision overflow must fail closed")
	}
	unchanged, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != math.MaxUint64 {
		t.Fatalf("overflowing save changed persisted revision: %d", unchanged.Revision)
	}
}

func TestProfileRootRejectsManagedAncestorSymlink(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "pool"))
	p, err := s.CreatePending("a", StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(p.CodexHome)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Rename(root, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProfileRoot(p); err == nil {
		t.Fatal("ProfileRoot must reject a symlink inside the managed accounts tree")
	}
}
