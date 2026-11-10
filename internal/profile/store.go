package profile

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"codexpool/internal/securefs"
)

var (
	// Alias never determines a filesystem path. Permit a verified email address
	// as the user-facing handle while retaining control/traversal protection.
	aliasRE     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+@-]{0,253}\z`)
	profileIDRE = regexp.MustCompile(`^p_[0-9a-f]{32}\z`)
)

var (
	ErrNotFound       = errors.New("profile not found")
	ErrNotReady       = errors.New("profile is not ready")
	ErrProfileChanged = errors.New("profile state changed before commit")
)

const managedRootMarker = ".codexpool-managed-root"
const managedRootMarkerContents = "codexpool-managed-root-v1\n"

type Store struct {
	Root string
}

func NewStore(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalizeRootParent(filepath.Clean(abs))
	if err != nil {
		return nil, err
	}
	if filepath.Dir(canonical) == canonical {
		return nil, fmt.Errorf("refuse filesystem root as CODEXPOOL_HOME: %q", canonical)
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		if resolvedHome, resolveErr := filepath.EvalSymlinks(home); resolveErr == nil && filepath.Clean(resolvedHome) == filepath.Clean(canonical) {
			return nil, fmt.Errorf("refuse user home directory as CODEXPOOL_HOME: %q", canonical)
		}
	}
	return &Store{Root: canonical}, nil
}

func canonicalizeRootParent(root string) (string, error) {
	parent := filepath.Dir(root)
	base := filepath.Base(root)
	resolvedParent, err := resolveExistingAncestors(parent)
	if err != nil {
		return "", fmt.Errorf("resolve managed root parent %q: %w", parent, err)
	}
	return filepath.Clean(filepath.Join(resolvedParent, base)), nil
}

func resolveExistingAncestors(path string) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0, 4)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %q", path)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func (s *Store) Init() error {
	if err := s.ensureManagedRoot(); err != nil {
		return err
	}
	for _, dir := range []string{
		filepath.Join(s.Root, "accounts"),
		filepath.Join(s.Root, "locks"),
		filepath.Join(s.Root, "shared"),
		filepath.Join(s.Root, "shared", "templates"),
	} {
		if err := securefs.ValidateManagedPath(s.Root, dir); err != nil {
			return err
		}
		if err := securefs.EnsurePrivateDir(dir); err != nil {
			return err
		}
		if err := securefs.ValidateManagedPath(s.Root, dir); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureManagedRoot() error {
	info, err := os.Lstat(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		if err := securefs.EnsurePrivateDir(s.Root); err != nil {
			return err
		}
		return securefs.AtomicWriteFile(filepath.Join(s.Root, managedRootMarker), []byte(managedRootMarkerContents))
	}
	if err != nil {
		return fmt.Errorf("inspect CODEXPOOL_HOME %q: %w", s.Root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("CODEXPOOL_HOME %q must be a real directory", s.Root)
	}

	markerPath := filepath.Join(s.Root, managedRootMarker)
	marker, markerErr := securefs.ReadPrivateFile(markerPath, 1024)
	if markerErr == nil {
		if string(marker) != managedRootMarkerContents {
			return fmt.Errorf("CODEXPOOL_HOME %q has an unrecognized management marker", s.Root)
		}
		return securefs.EnsurePrivateDir(s.Root)
	}
	if !errors.Is(markerErr, os.ErrNotExist) {
		return fmt.Errorf("validate CODEXPOOL_HOME marker: %w", markerErr)
	}

	legacy, legacyErr := s.looksLikeLegacyManagedRoot()
	if legacyErr != nil {
		return legacyErr
	}
	if !legacy {
		return fmt.Errorf("refuse to adopt existing directory %q as CODEXPOOL_HOME; choose a new empty path or an existing CodexPool-managed root", s.Root)
	}
	if err := securefs.EnsurePrivateDir(s.Root); err != nil {
		return err
	}
	return securefs.AtomicWriteFile(markerPath, []byte(managedRootMarkerContents))
}

func (s *Store) looksLikeLegacyManagedRoot() (bool, error) {
	for _, name := range []string{"accounts", "locks"} {
		path := filepath.Join(s.Root, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("inspect legacy CodexPool path %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, nil
		}
		if err := securefs.ValidateManagedPath(s.Root, path); err != nil {
			return false, err
		}
	}
	return true, nil
}

func ValidateAlias(alias string) error {
	if !aliasRE.MatchString(alias) || alias == "." || alias == ".." {
		return fmt.Errorf("invalid alias %q: use 1-63 ASCII letters, digits, dot, underscore or dash", alias)
	}
	if strings.ContainsAny(alias, "\x1b\r\n\t") {
		return fmt.Errorf("invalid alias %q: terminal control characters are not allowed", alias)
	}
	return nil
}

func (s *Store) Load() (Registry, error) {
	if err := s.Init(); err != nil {
		return Registry{}, err
	}
	path := s.registryPath()
	if err := securefs.ValidateManagedPath(s.Root, path); err != nil {
		return Registry{}, err
	}
	b, err := securefs.ReadPrivateFile(path, 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return Registry{SchemaVersion: SchemaVersion, Profiles: []Profile{}}, nil
	}
	if err != nil {
		return Registry{}, fmt.Errorf("read registry: %w", err)
	}
	var r Registry
	if err := decodeRegistryStrict(b, &r); err != nil {
		return Registry{}, fmt.Errorf("decode registry: %w", err)
	}
	if r.SchemaVersion != SchemaVersion {
		return Registry{}, fmt.Errorf("unsupported registry schema %d", r.SchemaVersion)
	}
	if err := s.validateRegistry(r); err != nil {
		return Registry{}, fmt.Errorf("validate registry: %w", err)
	}
	return r, nil
}

func decodeRegistryStrict(data []byte, out *Registry) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("registry contains multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("registry contains multiple JSON values")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("registry object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("registry contains duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("registry object did not close correctly")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("registry array did not close correctly")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}

func (s *Store) saveLocked(r Registry) error {
	if err := s.Init(); err != nil {
		return err
	}
	r.SchemaVersion = SchemaVersion
	if err := s.validateRegistry(r); err != nil {
		return fmt.Errorf("validate registry before save: %w", err)
	}
	if r.Revision == math.MaxUint64 {
		return errors.New("registry revision overflow")
	}
	r.Revision++
	sort.SliceStable(r.Profiles, func(i, j int) bool { return r.Profiles[i].ID < r.Profiles[j].ID })
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return securefs.AtomicWriteFile(s.registryPath(), b)
}

func (s *Store) validateRegistry(r Registry) error {
	seenIDs := make(map[string]struct{}, len(r.Profiles))
	readyIDs := make(map[string]struct{}, len(r.Profiles))
	seenAliases := make(map[string]struct{}, len(r.Profiles))
	seenHomes := make(map[string]struct{}, len(r.Profiles))
	for _, p := range r.Profiles {
		if !profileIDRE.MatchString(p.ID) {
			return fmt.Errorf("invalid profile id %q", p.ID)
		}
		if _, exists := seenIDs[p.ID]; exists {
			return fmt.Errorf("duplicate profile id %q", p.ID)
		}
		seenIDs[p.ID] = struct{}{}
		if err := ValidateAlias(p.Alias); err != nil {
			return err
		}
		aliasKey := strings.ToLower(p.Alias)
		if _, exists := seenAliases[aliasKey]; exists {
			return fmt.Errorf("duplicate profile alias %q", p.Alias)
		}
		seenAliases[aliasKey] = struct{}{}
		if p.CreatedAt.IsZero() {
			return fmt.Errorf("profile %s has zero createdAt", p.ID)
		}
		if p.AuthGeneration == 0 {
			return fmt.Errorf("profile %s has zero auth generation", p.ID)
		}
		if p.CredentialStore != StoreFile && p.CredentialStore != StoreKeyring {
			return fmt.Errorf("profile %s has unsupported credential store %q", p.ID, p.CredentialStore)
		}
		switch p.Lifecycle {
		case LifecyclePending, LifecycleReady, LifecycleReauthRequired, LifecycleLogoutPending, LifecyclePurgeLogoutPending, LifecycleDeleting:
		default:
			return fmt.Errorf("profile %s has invalid lifecycle %q", p.ID, p.Lifecycle)
		}
		if p.Lifecycle == LifecycleReady {
			if strings.TrimSpace(p.Identity.PlanType) == "" || p.Identity.ObservedAt.IsZero() {
				return fmt.Errorf("ready profile %s is missing verified identity metadata", p.ID)
			}
			readyIDs[p.ID] = struct{}{}
		}
		root, err := securefs.SafeChild(s.AccountsDir(), p.ID)
		if err != nil {
			return err
		}
		expectedHome, err := securefs.SafeChild(root, "codex-home")
		if err != nil {
			return err
		}
		if filepath.Clean(p.CodexHome) != filepath.Clean(expectedHome) {
			return fmt.Errorf("profile %s has unexpected CODEX_HOME", p.ID)
		}
		homeKey := filepath.Clean(p.CodexHome)
		if _, exists := seenHomes[homeKey]; exists {
			return fmt.Errorf("duplicate CODEX_HOME %q", homeKey)
		}
		seenHomes[homeKey] = struct{}{}
	}
	if r.ActiveProfileID != "" {
		if _, exists := seenIDs[r.ActiveProfileID]; !exists {
			return fmt.Errorf("active profile %q is not registered", r.ActiveProfileID)
		}
		if _, ready := readyIDs[r.ActiveProfileID]; !ready {
			return fmt.Errorf("active profile %q is not ready", r.ActiveProfileID)
		}
	}
	return nil
}

func (s *Store) WithRegistry(fn func(*Registry) error) error {
	if err := s.Init(); err != nil {
		return err
	}
	lock, err := securefs.TryLock(filepath.Join(s.locksDir(), "registry.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()

	r, err := s.Load()
	if err != nil {
		return err
	}
	if err := fn(&r); err != nil {
		return err
	}
	return s.saveLocked(r)
}

func (s *Store) CreatePending(alias string, credentialStore CredentialStore) (Profile, error) {
	if err := ValidateAlias(alias); err != nil {
		return Profile{}, err
	}
	if credentialStore != StoreFile && credentialStore != StoreKeyring {
		return Profile{}, fmt.Errorf("unsupported credential store %q", credentialStore)
	}

	id, err := randomID()
	if err != nil {
		return Profile{}, err
	}
	profileRoot, err := securefs.SafeChild(s.accountsDir(), id)
	if err != nil {
		return Profile{}, err
	}
	codexHome, err := securefs.SafeChild(profileRoot, "codex-home")
	if err != nil {
		return Profile{}, err
	}
	p := Profile{
		ID:              id,
		Alias:           alias,
		CreatedAt:       time.Now().UTC(),
		CodexHome:       codexHome,
		CredentialStore: credentialStore,
		AuthGeneration:  1,
		Lifecycle:       LifecyclePending,
	}

	err = s.WithRegistry(func(r *Registry) error {
		for _, existing := range r.Profiles {
			if strings.EqualFold(existing.Alias, alias) {
				return fmt.Errorf("alias %q already exists", alias)
			}
			if filepath.Clean(existing.CodexHome) == filepath.Clean(codexHome) {
				return fmt.Errorf("profile path collision")
			}
		}
		for _, dir := range []string{profileRoot, codexHome, filepath.Join(profileRoot, "cache")} {
			if err := securefs.ValidateManagedPath(s.Root, dir); err != nil {
				return err
			}
			if err := securefs.EnsurePrivateDir(dir); err != nil {
				return err
			}
			if err := securefs.ValidateManagedPath(s.Root, dir); err != nil {
				return err
			}
		}
		config := []byte(fmt.Sprintf("cli_auth_credentials_store = %q\n", credentialStore))
		if err := securefs.AtomicWriteFile(filepath.Join(codexHome, "config.toml"), config); err != nil {
			return err
		}
		r.Profiles = append(r.Profiles, p)
		return nil
	})
	if err != nil {
		return s.handleCreateFailure(p, profileRoot, err)
	}
	return p, nil
}

func (s *Store) handleCreateFailure(p Profile, profileRoot string, createErr error) (Profile, error) {
	if securefs.AtomicWriteCommitState(createErr) == securefs.CommitDurabilityUncertain {
		registry, loadErr := s.Load()
		if loadErr != nil {
			return p, fmt.Errorf("profile creation outcome is durability-uncertain and registry could not be re-read; Profile data was retained for recovery: %w (registry read: %v)", createErr, loadErr)
		}
		for _, current := range registry.Profiles {
			if current.ID == p.ID {
				return current, fmt.Errorf("profile registry update is visible but its crash durability is uncertain; Profile data was retained for recovery: %w", createErr)
			}
		}
	}
	if cleanupErr := securefs.RemoveTree(s.accountsDir(), profileRoot); cleanupErr != nil {
		return p, fmt.Errorf("profile creation failed and unregistered Profile data could not be cleaned up: %w (cleanup: %v)", createErr, cleanupErr)
	}
	return Profile{}, createErr
}

func (s *Store) Resolve(aliasOrID string) (Profile, error) {
	r, err := s.Load()
	if err != nil {
		return Profile{}, err
	}
	for _, p := range r.Profiles {
		if p.ID == aliasOrID || strings.EqualFold(p.Alias, aliasOrID) {
			return p, nil
		}
	}
	return Profile{}, ErrNotFound
}

func (s *Store) Active() (Profile, error) {
	r, err := s.Load()
	if err != nil {
		return Profile{}, err
	}
	if r.ActiveProfileID == "" {
		return Profile{}, ErrNotFound
	}
	for _, p := range r.Profiles {
		if p.ID == r.ActiveProfileID {
			return p, nil
		}
	}
	return Profile{}, ErrNotFound
}

func (s *Store) SetActive(aliasOrID string) (Profile, error) {
	p, err := s.Resolve(aliasOrID)
	if err != nil {
		return Profile{}, err
	}
	return s.SetActiveExpected(p)
}

func (s *Store) SetActiveExpected(expected Profile) (Profile, error) {
	var selected Profile
	err := s.WithRegistry(func(r *Registry) error {
		for _, p := range r.Profiles {
			if p.ID != expected.ID {
				continue
			}
			if !SameCommitIdentity(p, expected) {
				return fmt.Errorf("%w: %s", ErrProfileChanged, expected.ID)
			}
			if p.Lifecycle != LifecycleReady {
				return fmt.Errorf("%w: %s is %s", ErrNotReady, p.Alias, p.Lifecycle)
			}
			selected = p
			r.ActiveProfileID = p.ID
			return nil
		}
		return ErrNotFound
	})
	return selected, err
}

func (s *Store) List() ([]Profile, string, error) {
	r, err := s.Load()
	if err != nil {
		return nil, "", err
	}
	out := append([]Profile(nil), r.Profiles...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Alias) < strings.ToLower(out[j].Alias)
	})
	return out, r.ActiveProfileID, nil
}

func (s *Store) OperationLockPath(profileID string) (string, error) {
	if !profileIDRE.MatchString(profileID) {
		return "", fmt.Errorf("invalid profile id %q", profileID)
	}
	return securefs.SafeChild(s.locksDir(), "profile-"+profileID+".lock")
}

func (s *Store) Update(aliasOrID string, mutate func(*Profile) error) (Profile, error) {
	var updated Profile
	err := s.WithRegistry(func(r *Registry) error {
		for i := range r.Profiles {
			if r.Profiles[i].ID == aliasOrID || strings.EqualFold(r.Profiles[i].Alias, aliasOrID) {
				if err := mutate(&r.Profiles[i]); err != nil {
					return err
				}
				updated = r.Profiles[i]
				return nil
			}
		}
		return ErrNotFound
	})
	return updated, err
}

func SameCommitIdentity(current, expected Profile) bool {
	return current.ID == expected.ID &&
		current.AuthGeneration == expected.AuthGeneration &&
		filepath.Clean(current.CodexHome) == filepath.Clean(expected.CodexHome) &&
		current.CredentialStore == expected.CredentialStore &&
		current.Lifecycle == expected.Lifecycle
}

func (s *Store) UpdateExpected(expected Profile, mutate func(*Profile) error) (Profile, error) {
	var updated Profile
	err := s.WithRegistry(func(r *Registry) error {
		for i := range r.Profiles {
			if r.Profiles[i].ID != expected.ID {
				continue
			}
			if !SameCommitIdentity(r.Profiles[i], expected) {
				return fmt.Errorf("%w: %s", ErrProfileChanged, expected.ID)
			}
			if err := mutate(&r.Profiles[i]); err != nil {
				return err
			}
			if r.Profiles[i].Lifecycle != LifecycleReady && r.ActiveProfileID == r.Profiles[i].ID {
				r.ActiveProfileID = ""
			}
			updated = r.Profiles[i]
			return nil
		}
		return ErrNotFound
	})
	return updated, err
}

func (s *Store) Rename(aliasOrID, newAlias string) (Profile, error) {
	if err := ValidateAlias(newAlias); err != nil {
		return Profile{}, err
	}
	var renamed Profile
	err := s.WithRegistry(func(r *Registry) error {
		for _, p := range r.Profiles {
			if strings.EqualFold(p.Alias, newAlias) && p.ID != aliasOrID && !strings.EqualFold(p.Alias, aliasOrID) {
				return fmt.Errorf("alias %q already exists", newAlias)
			}
		}
		for i := range r.Profiles {
			if r.Profiles[i].ID == aliasOrID || strings.EqualFold(r.Profiles[i].Alias, aliasOrID) {
				r.Profiles[i].Alias = newAlias
				renamed = r.Profiles[i]
				return nil
			}
		}
		return ErrNotFound
	})
	return renamed, err
}

func (s *Store) ProfileRoot(p Profile) (string, error) {
	if !profileIDRE.MatchString(p.ID) {
		return "", fmt.Errorf("invalid profile id %q", p.ID)
	}
	root, err := securefs.SafeChild(s.AccountsDir(), p.ID)
	if err != nil {
		return "", err
	}
	expectedHome, err := securefs.SafeChild(root, "codex-home")
	if err != nil {
		return "", err
	}
	if filepath.Clean(p.CodexHome) != filepath.Clean(expectedHome) {
		return "", fmt.Errorf("profile %s has unexpected CODEX_HOME", p.ID)
	}
	if err := securefs.ValidateManagedPath(s.Root, root); err != nil {
		return "", err
	}
	if err := securefs.ValidateManagedPath(s.Root, expectedHome); err != nil {
		return "", err
	}
	return root, nil
}

func (s *Store) PurgeData(p Profile) error {
	root, err := s.ProfileRoot(p)
	if err != nil {
		return err
	}
	return securefs.RemoveTree(s.AccountsDir(), root)
}

func (s *Store) RemoveRegistration(aliasOrID string) (Profile, error) {
	var removed Profile
	err := s.WithRegistry(func(r *Registry) error {
		for i, p := range r.Profiles {
			if p.ID == aliasOrID || strings.EqualFold(p.Alias, aliasOrID) {
				removed = p
				r.Profiles = append(r.Profiles[:i], r.Profiles[i+1:]...)
				if r.ActiveProfileID == p.ID {
					r.ActiveProfileID = ""
				}
				return nil
			}
		}
		return ErrNotFound
	})
	return removed, err
}

func (s *Store) RegistryPath() string { return filepath.Join(s.Root, "registry.json") }
func (s *Store) AccountsDir() string  { return filepath.Join(s.Root, "accounts") }
func (s *Store) LocksDir() string     { return filepath.Join(s.Root, "locks") }

func (s *Store) GlobalLoginLockPath() (string, error) {
	return securefs.SafeChild(s.LocksDir(), "login.lock")
}

func (s *Store) registryPath() string { return s.RegistryPath() }
func (s *Store) accountsDir() string  { return s.AccountsDir() }
func (s *Store) locksDir() string     { return s.LocksDir() }

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate profile id: %w", err)
	}
	return "p_" + hex.EncodeToString(b), nil
}
