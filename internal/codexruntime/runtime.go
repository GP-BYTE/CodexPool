package codexruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"codexpool/internal/apprpc"
	"codexpool/internal/profile"
	"codexpool/internal/scheduler"
	"codexpool/internal/securefs"
)

const DefaultMacBinary = "/Applications/ChatGPT.app/Contents/Resources/codex"

// Newer ChatGPT releases bundle the CLI inside its own signed app.
const BundledMacBinary = "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex"

// Reviewed only for official login and subsequent validated account reads.
// See docs/evidence/codex-0.160.0-login/README.md.
const ReviewedLoginVersion = "0.160.0"

var (
	ErrProfileChanged             = profile.ErrProfileChanged
	ErrUnsupportedRuntime         = errors.New("unsupported Codex runtime version")
	ErrUnsupportedCredentialStore = errors.New("credential store is not supported by this preview")
	semanticVersionRE             = regexp.MustCompile(`(?:^|[^0-9A-Za-z])(\d+\.\d+\.\d+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?)(?:$|[^0-9A-Za-z.-])`)
)

type Runtime struct {
	Binary          string
	Store           *profile.Store
	Guard           *scheduler.Guard
	ExpectedVersion string
	discoveryErr    error
}

type AppServer struct {
	Cmd           *exec.Cmd
	Client        *apprpc.Client
	stdin         io.WriteCloser
	waitCh        chan error
	stderrDone    chan struct{}
	lease         *securefs.Lock
	leaseMu       sync.Mutex
	leaseWhenDone bool
	resourceOnce  sync.Once
	resourceDone  chan struct{}
	resourceErr   error
	reapTimeout   time.Duration
	closeOnce     sync.Once
	closeDone     chan struct{}
	closeErr      error
}

// cappedBuffer keeps at most limit bytes while still reporting successful
// writes to os/exec. Version output is tiny in normal operation; the cap keeps
// a broken or substituted executable from growing memory without bound.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = b.buf.Write(p[:remaining])
	}
	if remaining < len(p) {
		b.truncated = true
	}
	return written, nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }
func (b *cappedBuffer) Len() int       { return b.buf.Len() }

func New(binary string, store *profile.Store) (*Runtime, error) {
	if store == nil {
		return nil, errors.New("profile store is required")
	}
	if strings.TrimSpace(binary) == "" {
		return nil, errors.New("Codex binary path is required")
	}
	abs, err := filepath.Abs(binary)
	if err != nil {
		return nil, fmt.Errorf("resolve Codex binary: %w", err)
	}
	return &Runtime{Binary: filepath.Clean(abs), Store: store, Guard: scheduler.New(store)}, nil
}

func NewUnavailable(store *profile.Store, discoveryErr error) (*Runtime, error) {
	if store == nil {
		return nil, errors.New("profile store is required")
	}
	if discoveryErr == nil {
		discoveryErr = errors.New("Codex binary is unavailable")
	}
	return &Runtime{Store: store, Guard: scheduler.New(store), discoveryErr: discoveryErr}, nil
}

func (r *Runtime) BinaryAvailabilityError() error {
	if r == nil {
		return errors.New("Codex runtime is not configured")
	}
	if r.discoveryErr != nil {
		return r.discoveryErr
	}
	if strings.TrimSpace(r.Binary) == "" {
		return errors.New("Codex binary is unavailable")
	}
	return nil
}

func DefaultBinary() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODEXPOOL_CODEX_BIN")); configured != "" {
		return filepath.Abs(configured)
	}
	if runtime.GOOS == "darwin" {
		return discoverMacBinary([]string{BundledMacBinary, DefaultMacBinary})
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		return "", fmt.Errorf("find Codex CLI: %w", err)
	}
	return filepath.Abs(path)
}

func discoverMacBinary(candidates []string) (string, error) {
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	return "", errors.New("找不到 ChatGPT 内置 Codex 程序；请检查应用安装，或用 CODEXPOOL_CODEX_BIN 指定 Codex 可执行文件")
}

func (r *Runtime) Version(ctx context.Context) (string, error) {
	if err := r.BinaryAvailabilityError(); err != nil {
		return "", fmt.Errorf("Codex binary unavailable: %w", err)
	}
	probeRoot := filepath.Join(r.Store.Root, "runtime-probe")
	probeHome := filepath.Join(probeRoot, "codex-home")
	if err := securefs.EnsurePrivateDir(probeRoot); err != nil {
		return "", err
	}
	if err := securefs.ValidateManagedPath(r.Store.Root, probeHome); err != nil {
		return "", err
	}
	if err := securefs.EnsurePrivateDir(probeHome); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, r.Binary, "--version")
	cmd.Env = StrictProfileEnv(os.Environ(), probeHome)
	stdout := cappedBuffer{limit: 64 << 10}
	stderr := cappedBuffer{limit: 64 << 10}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("probe Codex version: %w", err)
	}
	if stdout.truncated || stderr.truncated {
		return "", errors.New("Codex version output exceeded 65536 bytes")
	}
	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n" + stderr.String()
	}
	version := strings.TrimSpace(output)
	if version == "" {
		return "", errors.New("Codex version output was empty")
	}
	return version, nil
}

func ParseSemanticVersion(output string) (string, error) {
	match := semanticVersionRE.FindStringSubmatch(strings.TrimSpace(output))
	if len(match) != 2 {
		return "", fmt.Errorf("could not parse Codex semantic version from %q", strings.TrimSpace(output))
	}
	return match[1], nil
}

// ProbeSemanticVersion verifies that the configured Codex binary is available
// and returns its parsed semantic version without applying the pinned protocol
// evidence gate. Callers that use a strictly validated read-only protocol can
// attempt forward-compatible reads while mutating/interactive operations remain
// pinned through RequireCompatible.
func (r *Runtime) ProbeSemanticVersion(ctx context.Context) (string, error) {
	if err := r.BinaryAvailabilityError(); err != nil {
		return "", fmt.Errorf("Codex binary unavailable: %w", err)
	}
	raw, err := r.Version(ctx)
	if err != nil {
		return "", err
	}
	return ParseSemanticVersion(raw)
}

func (r *Runtime) RequireCompatible(ctx context.Context) (string, error) {
	if err := r.BinaryAvailabilityError(); err != nil {
		return "", fmt.Errorf("Codex binary unavailable: %w", err)
	}
	if strings.TrimSpace(r.ExpectedVersion) == "" {
		return "", nil
	}
	actual, err := r.ProbeSemanticVersion(ctx)
	if err != nil {
		return "", err
	}
	if actual != r.ExpectedVersion {
		return actual, fmt.Errorf("%w: evidence=%s runtime=%s", ErrUnsupportedRuntime, r.ExpectedVersion, actual)
	}
	return actual, nil
}

func (r *Runtime) RequireLoginCompatible(ctx context.Context) (string, error) {
	actual, err := r.RequireCompatible(ctx)
	if errors.Is(err, ErrUnsupportedRuntime) && actual == ReviewedLoginVersion {
		return actual, nil
	}
	return actual, err
}

func (r *Runtime) StartAppServer(ctx context.Context, p profile.Profile) (*AppServer, string, error) {
	lock, err := r.Guard.TryAcquire(p.ID)
	if err != nil {
		return nil, "", err
	}
	current, err := r.resolveCurrentGeneration(p)
	if err != nil {
		_ = lock.Close()
		return nil, "", err
	}
	if current.Lifecycle != profile.LifecycleReady && current.Lifecycle != profile.LifecyclePending {
		_ = lock.Close()
		return nil, "", fmt.Errorf("%w: profile %s is %s", profile.ErrNotReady, current.Alias, current.Lifecycle)
	}
	if err := r.validateProfileConfig(current); err != nil {
		_ = lock.Close()
		return nil, "", err
	}
	// Account/quota reads validate the wire payload before accepting it. Do not
	// make a harmless ChatGPT/Codex app upgrade disable the entire dashboard
	// before that compatibility check can run. Interactive/login/logout paths
	// remain pinned to reviewed evidence, with a separate reviewed login gate.
	runtimeVersion, err := r.ProbeSemanticVersion(ctx)
	if err != nil {
		_ = lock.Close()
		return nil, runtimeVersion, err
	}
	server, err := r.startAppServerUnlocked(ctx, current, lock)
	if err != nil {
		_ = lock.Close()
		return nil, runtimeVersion, err
	}
	return server, runtimeVersion, nil
}

func (r *Runtime) startAppServerUnlocked(ctx context.Context, p profile.Profile, lease *securefs.Lock) (*AppServer, error) {
	profileRoot := filepath.Dir(p.CodexHome)
	neutralDir := filepath.Join(profileRoot, "cache", "query-cwd")
	if err := securefs.ValidateManagedPath(profileRoot, neutralDir); err != nil {
		return nil, err
	}
	if err := securefs.EnsurePrivateDir(neutralDir); err != nil {
		return nil, err
	}
	if err := securefs.ValidateManagedPath(profileRoot, neutralDir); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, r.Binary, "app-server")
	configureOwnedProcessTree(cmd)
	cmd.Dir = neutralDir
	cmd.Env = StrictProfileEnv(os.Environ(), p.CodexHome)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("create app-server stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("create app-server stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}

	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
		close(stderrDone)
	}()
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	return &AppServer{
		Cmd:          cmd,
		Client:       apprpc.NewClient(stdout, stdin, apprpc.DefaultMaxFrameBytes),
		stdin:        stdin,
		waitCh:       waitCh,
		stderrDone:   stderrDone,
		lease:        lease,
		resourceDone: make(chan struct{}),
		reapTimeout:  time.Second,
		closeDone:    make(chan struct{}),
	}, nil
}

func (s *AppServer) ResourceDone() <-chan struct{} {
	if s == nil || s.resourceDone == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return s.resourceDone
}

// ReleaseLease releases the Profile lease only after all owned process resources
// are finalized. If cleanup is still pending, AppServer retains the lease and
// releases it when the late process reap completes.
func (s *AppServer) ReleaseLease() error {
	if s == nil {
		return nil
	}
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if s.lease == nil {
		return nil
	}
	select {
	case <-s.resourceDone:
		err := s.lease.Close()
		s.lease = nil
		return err
	default:
		s.leaseWhenDone = true
		return nil
	}
}

func (s *AppServer) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.closeDone == nil {
		s.closeDone = make(chan struct{})
	}
	s.closeOnce.Do(func() {
		go func() {
			s.closeErr = s.closeInternal(ctx)
			close(s.closeDone)
		}()
	})
	select {
	case <-s.closeDone:
		return s.closeErr
	case <-ctx.Done():
		return fmt.Errorf("wait for Codex app-server close: %w", ctx.Err())
	}
}

func (s *AppServer) closeInternal(ctx context.Context) error {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.waitCh == nil {
		if err := s.ensureOwnedProcessTreeStopped(); err != nil {
			return err
		}
		return s.finalizeResources(nil)
	}

	var waitErr error
	select {
	case waitErr = <-s.waitCh:
	case <-ctx.Done():
		if s.Client != nil {
			_ = s.Client.Close()
		}
		if err := terminateOwnedProcessTree(s.Cmd); err != nil {
			return fmt.Errorf("terminate Codex app-server process tree; Profile lease remains held: %w", err)
		}
		reapTimeout := s.reapTimeout
		if reapTimeout <= 0 {
			reapTimeout = time.Second
		}
		killWait := time.NewTimer(reapTimeout)
		defer killWait.Stop()
		select {
		case waitErr = <-s.waitCh:
		case <-killWait.C:
			go func() {
				lateWaitErr := <-s.waitCh
				if treeErr := s.ensureOwnedProcessTreeStopped(); treeErr != nil {
					return
				}
				_ = s.finalizeResources(lateWaitErr)
			}()
			return fmt.Errorf("Codex app-server did not reap after process-tree kill; Profile lease remains held: %w", ctx.Err())
		}
		waitErr = errors.Join(waitErr, fmt.Errorf("stop Codex app-server: %w", ctx.Err()))
	}
	if err := s.ensureOwnedProcessTreeStopped(); err != nil {
		return err
	}
	return s.finalizeResources(waitErr)
}

func (s *AppServer) ensureOwnedProcessTreeStopped() error {
	// A successful SIGKILL to the dedicated process group (or ESRCH when it no
	// longer exists) is the portable termination boundary. The direct child is
	// separately reaped through waitCh, and finalizeResources waits for inherited
	// stderr descriptors to drain. Avoid kill(-pgid, 0) liveness probes here:
	// sandboxed/race builds may return EPERM even for a group we just terminated.
	if err := terminateOwnedProcessTree(s.Cmd); err != nil {
		return fmt.Errorf("terminate Codex app-server process tree; Profile lease remains held: %w", err)
	}
	return nil
}

func (s *AppServer) finalizeResources(waitErr error) error {
	s.resourceOnce.Do(func() {
		if s.Client != nil {
			_ = s.Client.Close()
		}
		var drainErr error
		if s.stderrDone != nil {
			drainTimer := time.NewTimer(time.Second)
			defer drainTimer.Stop()
			select {
			case <-s.stderrDone:
			case <-drainTimer.C:
				drainErr = errors.New("Codex app-server stderr drain did not finish after process exit")
			}
		}
		s.resourceErr = errors.Join(waitErr, drainErr)
		s.leaseMu.Lock()
		if s.leaseWhenDone && s.lease != nil {
			s.resourceErr = errors.Join(s.resourceErr, s.lease.Close())
			s.lease = nil
		}
		close(s.resourceDone)
		s.leaseMu.Unlock()
	})
	return s.resourceErr
}

func (r *Runtime) RunInteractive(ctx context.Context, p profile.Profile, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return r.RunInteractiveValidated(ctx, p, args, stdin, stdout, stderr, nil)
}

func (r *Runtime) RunInteractiveValidated(ctx context.Context, p profile.Profile, args []string, stdin io.Reader, stdout, stderr io.Writer, validate func(profile.Profile) error) error {
	if err := ValidateRunArgs(args); err != nil {
		return err
	}
	if _, err := r.RequireCompatible(ctx); err != nil {
		return err
	}
	lock, err := r.Guard.TryAcquire(p.ID)
	if err != nil {
		return err
	}
	defer lock.Close()

	current, err := r.resolveCurrentGeneration(p)
	if err != nil {
		return err
	}
	if current.Lifecycle != profile.LifecycleReady {
		return fmt.Errorf("%w: profile %s is %s", profile.ErrNotReady, current.Alias, current.Lifecycle)
	}
	if err := r.validateProfileConfig(current); err != nil {
		return err
	}
	if validate != nil {
		if err := validate(current); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	configureGracefulCancellation(cmd)
	cmd.Env = StrictProfileEnv(os.Environ(), current.CodexHome)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (r *Runtime) LoginInteractive(ctx context.Context, p profile.Profile, deviceAuth bool, stdin io.Reader, stdout, stderr io.Writer) error {
	_, err := r.LoginAnd(ctx, p, deviceAuth, stdin, stdout, stderr, nil)
	return err
}

func (r *Runtime) LoginAnd(ctx context.Context, p profile.Profile, deviceAuth bool, stdin io.Reader, stdout, stderr io.Writer, before func(profile.Profile) (profile.Profile, error)) (profile.Profile, error) {
	if _, err := r.RequireLoginCompatible(ctx); err != nil {
		return profile.Profile{}, err
	}
	loginLockPath, err := r.Store.GlobalLoginLockPath()
	if err != nil {
		return profile.Profile{}, err
	}
	loginLock, err := securefs.TryLock(loginLockPath)
	if err != nil {
		return profile.Profile{}, fmt.Errorf("login coordinator: %w", err)
	}
	defer loginLock.Close()

	lock, err := r.Guard.TryAcquire(p.ID)
	if err != nil {
		return profile.Profile{}, err
	}
	defer lock.Close()

	current, err := r.resolveCurrentGeneration(p)
	if err != nil {
		return profile.Profile{}, err
	}
	if before != nil {
		current, err = before(current)
		if err != nil {
			return profile.Profile{}, err
		}
		if current.ID != p.ID || filepath.Clean(current.CodexHome) != filepath.Clean(p.CodexHome) || current.CredentialStore != p.CredentialStore {
			return profile.Profile{}, fmt.Errorf("%w: login mutation changed immutable Profile identity", ErrProfileChanged)
		}
	}
	if err := r.validateProfileConfig(current); err != nil {
		return current, err
	}
	args := []string{"login"}
	if deviceAuth {
		args = append(args, "--device-auth")
	}
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	configureGracefulCancellation(cmd)
	cmd.Env = StrictProfileEnv(os.Environ(), current.CodexHome)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return current, err
	}
	return current, nil
}

func (r *Runtime) LogoutInteractive(ctx context.Context, p profile.Profile, stdin io.Reader, stdout, stderr io.Writer) error {
	return r.LogoutAnd(ctx, p, stdin, stdout, stderr, nil)
}

func (r *Runtime) LogoutAnd(ctx context.Context, p profile.Profile, stdin io.Reader, stdout, stderr io.Writer, after func() error) error {
	_, err := r.LogoutTransition(ctx, p, stdin, stdout, stderr, nil, func(profile.Profile) error {
		if after != nil {
			return after()
		}
		return nil
	})
	return err
}

func (r *Runtime) LogoutTransition(ctx context.Context, p profile.Profile, stdin io.Reader, stdout, stderr io.Writer, before func(profile.Profile) (profile.Profile, error), after func(profile.Profile) error) (profile.Profile, error) {
	if _, err := r.RequireCompatible(ctx); err != nil {
		return profile.Profile{}, err
	}
	lock, err := r.Guard.TryAcquire(p.ID)
	if err != nil {
		return profile.Profile{}, err
	}
	defer lock.Close()

	current, err := r.resolveCurrentGeneration(p)
	if err != nil {
		return profile.Profile{}, err
	}
	if before != nil {
		current, err = before(current)
		if err != nil {
			return current, err
		}
		if current.ID != p.ID || filepath.Clean(current.CodexHome) != filepath.Clean(p.CodexHome) || current.CredentialStore != p.CredentialStore {
			return current, fmt.Errorf("%w: logout transition changed immutable Profile identity", ErrProfileChanged)
		}
	}
	if err := r.validateProfileConfig(current); err != nil {
		return current, err
	}
	if err := r.logoutUnlocked(ctx, current, stdin, stdout, stderr); err != nil {
		return current, err
	}
	if after != nil {
		if err := after(current); err != nil {
			return current, err
		}
	}
	return current, nil
}

func (r *Runtime) logoutUnlocked(ctx context.Context, p profile.Profile, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, r.Binary, "logout")
	configureGracefulCancellation(cmd)
	cmd.Env = StrictProfileEnv(os.Environ(), p.CodexHome)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (r *Runtime) resolveCurrentGeneration(expected profile.Profile) (profile.Profile, error) {
	current, err := r.Store.Resolve(expected.ID)
	if err != nil {
		return profile.Profile{}, err
	}
	if !profile.SameCommitIdentity(current, expected) {
		return profile.Profile{}, fmt.Errorf("%w: profile %s no longer matches expected generation/home/store/lifecycle", ErrProfileChanged, expected.ID)
	}
	if _, err := r.Store.ProfileRoot(current); err != nil {
		return profile.Profile{}, fmt.Errorf("validate managed Profile path: %w", err)
	}
	return current, nil
}

func (r *Runtime) validateProfileConfig(p profile.Profile) error {
	if p.CredentialStore != profile.StoreFile {
		return fmt.Errorf("%w: %q requires platform keyring validation before authenticated use", ErrUnsupportedCredentialStore, p.CredentialStore)
	}
	if _, err := r.Store.ProfileRoot(p); err != nil {
		return err
	}
	path := filepath.Join(p.CodexHome, "config.toml")
	if err := securefs.ValidateManagedPath(filepath.Dir(p.CodexHome), path); err != nil {
		return err
	}
	data, err := securefs.ReadPrivateFile(path, 64<<10)
	if err != nil {
		return fmt.Errorf("read Profile config: %w", err)
	}
	return validateCredentialStoreConfig(data, p.CredentialStore)
}

func validateCredentialStoreConfig(data []byte, expected profile.CredentialStore) error {
	const key = "cli_auth_credentials_store"
	found := false
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Once a TOML table starts, later bare assignments belong to that table,
		// not to the document root where Codex reads cli_auth_credentials_store.
		if strings.HasPrefix(line, "[") {
			break
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		if found {
			return fmt.Errorf("Profile config contains duplicate top-level %s", key)
		}
		found = true
		value = strings.TrimSpace(value)
		if len(value) < 2 || (value[0] != '"' && value[0] != '\'') {
			return fmt.Errorf("Profile config has invalid top-level %s", key)
		}
		quote := value[0]
		end := strings.IndexByte(value[1:], quote)
		if end < 0 {
			return fmt.Errorf("Profile config has unterminated top-level %s", key)
		}
		got := value[1 : end+1]
		trailing := strings.TrimSpace(value[end+2:])
		if trailing != "" && !strings.HasPrefix(trailing, "#") {
			return fmt.Errorf("Profile config has invalid trailing content for top-level %s", key)
		}
		if got != string(expected) {
			return fmt.Errorf("Profile config does not match registered credential store %q", expected)
		}
	}
	if !found {
		return fmt.Errorf("Profile config is missing top-level %s for registered credential store %q", key, expected)
	}
	return nil
}

var strippedEnvironmentKeys = map[string]struct{}{
	"CODEX_HOME":                        {},
	"CODEX_API_KEY":                     {},
	"CODEX_ACCESS_TOKEN":                {},
	"CODEX_REFRESH_TOKEN_URL_OVERRIDE":  {},
	"CODEX_REVOKE_TOKEN_URL_OVERRIDE":   {},
	"CODEX_BASE_URL":                    {},
	"CODEX_REMOTE_BASE_URL":             {},
	"OPENAI_API_KEY":                    {},
	"OPENAI_BASE_URL":                   {},
	"OPENAI_ORG_ID":                     {},
	"OPENAI_ORGANIZATION":               {},
	"OPENAI_PROJECT_ID":                 {},
	"CHATGPT_BASE_URL":                  {},
	"CHATGPT_ACCOUNT_ID":                {},
	"CODEX_FORCE_CHATGPT_ACCOUNT_ID":    {},
	"CODEX_MANAGED_CONFIG_URL_OVERRIDE": {},
	"CODEX_MANAGED_POLICY_URL_OVERRIDE": {},
	"CODEX_GATEWAY_BASE_URL_OVERRIDE":   {},
	"CODEX_AUTH_BASE_URL_OVERRIDE":      {},
	"CODEX_LOGIN_SERVER_PORT_OVERRIDE":  {},
	"CODEX_LOGIN_SERVER_BIND_OVERRIDE":  {},
	"CODEX_USE_EXTERNAL_AUTH":           {},
	"CODEX_EXTERNAL_AUTH_TOKEN":         {},
	"CODEX_EXTERNAL_AUTH_ACCOUNT_ID":    {},
	"CODEX_EXTERNAL_AUTH_PLAN_TYPE":     {},
}

func StrictProfileEnv(parent []string, codexHome string) []string {
	out := make([]string, 0, len(parent)+1)
	for _, entry := range parent {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if _, blocked := strippedEnvironmentKeys[strings.ToUpper(key)]; blocked {
			continue
		}
		out = append(out, entry)
	}
	out = append(out, "CODEX_HOME="+codexHome)
	return out
}

func ValidateRunArgs(args []string) error {
	commandSeen := false
	skipValue := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if skipValue {
			skipValue = false
			continue
		}
		switch {
		case arg == "-c" || arg == "--config" || strings.HasPrefix(arg, "-c=") || strings.HasPrefix(arg, "--config=") || (strings.HasPrefix(arg, "-c") && len(arg) > 2):
			return errors.New("Codex -c/--config overrides are blocked because they can change authentication, provider, or routing")
		case arg == "-p" || arg == "--profile" || strings.HasPrefix(arg, "-p=") || strings.HasPrefix(arg, "--profile=") || (strings.HasPrefix(arg, "-p") && len(arg) > 2):
			return errors.New("Codex -p/--profile is blocked because CodexPool already owns the isolated Profile configuration")
		case arg == "--oss":
			return errors.New("Codex --oss is blocked because it switches away from the selected ChatGPT Profile provider")
		case arg == "--local-provider" || strings.HasPrefix(arg, "--local-provider="):
			return errors.New("Codex --local-provider is blocked because it changes the selected provider")
		case arg == "--ignore-user-config":
			return errors.New("Codex --ignore-user-config is blocked because CodexPool requires the validated Profile config")
		case arg == "--enable" || strings.HasPrefix(arg, "--enable=") || arg == "--disable" || strings.HasPrefix(arg, "--disable="):
			return errors.New("Codex --enable/--disable feature overrides are blocked because they are equivalent to configuration overrides")
		case arg == "--remote" || strings.HasPrefix(arg, "--remote="):
			return errors.New("Codex --remote is blocked because it routes the session away from the selected local Profile runtime")
		case arg == "--remote-auth-token-env" || strings.HasPrefix(arg, "--remote-auth-token-env="):
			return errors.New("Codex --remote-auth-token-env is blocked because it introduces alternate remote authentication")
		}

		if commandSeen {
			continue
		}
		if codexOptionConsumesValue(arg) {
			skipValue = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "login" || arg == "logout" {
			return fmt.Errorf("%q must be run through CodexPool account lifecycle commands", arg)
		}
		commandSeen = true
	}
	return nil
}

func codexOptionConsumesValue(arg string) bool {
	switch arg {
	case "-m", "--model", "-i", "--image", "-C", "--cd", "--add-dir", "-s", "--sandbox", "-a", "--ask-for-approval", "--output-schema", "-o", "--output-last-message", "--color":
		return true
	default:
		return false
	}
}
