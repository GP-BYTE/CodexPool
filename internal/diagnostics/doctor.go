package diagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"codexpool/internal/codexruntime"
	"codexpool/internal/profile"
	"codexpool/internal/securefs"
)

type Evidence struct {
	ResearchDate         string `json:"researchDate"`
	CodexVersion         string `json:"codexVersion"`
	BinaryPath           string `json:"binaryPath"`
	SourceCommitReviewed string `json:"sourceCommitReviewed"`
}

const EvidenceManifestSHA256 = "32b2700f2922f7aaf88c39feec2d556dc2e5de5a6e997d61d0842794d645a029"

var BuiltInEvidence = Evidence{
	ResearchDate:         "2026-09-13",
	CodexVersion:         "0.154.0-alpha.6.2",
	BinaryPath:           "/Applications/ChatGPT.app/Contents/Resources/codex",
	SourceCommitReviewed: "not-applicable-local-generated-protocol-evidence",
}

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type Report struct {
	Platform       string    `json:"platform"`
	Architecture   string    `json:"architecture"`
	CodexPoolRoot  string    `json:"codexPoolRoot"`
	CodexBinary    string    `json:"codexBinary"`
	RuntimeVersion string    `json:"runtimeVersion,omitempty"`
	Evidence       *Evidence `json:"evidence,omitempty"`
	Checks         []Check   `json:"checks"`
}

func Run(ctx context.Context, store *profile.Store, rt *codexruntime.Runtime, evidenceManifest string, probeRuntime bool) Report {
	report := Report{
		Platform:      goruntime.GOOS,
		Architecture:  goruntime.GOARCH,
		CodexPoolRoot: store.Root,
		CodexBinary:   rt.Binary,
	}

	report.Checks = append(report.Checks, checkBinary(rt))
	if evidence, err := loadEvidence(evidenceManifest); err != nil {
		report.Checks = append(report.Checks, Check{Name: "protocol-evidence", Status: "fail", Detail: err.Error()})
	} else {
		report.Evidence = &evidence
		report.Checks = append(report.Checks, Check{
			Name:   "protocol-evidence",
			Status: "ok",
			Detail: fmt.Sprintf("codex %s evidence dated %s", evidence.CodexVersion, evidence.ResearchDate),
		})
	}

	if probeRuntime {
		if rawVersion, err := rt.Version(ctx); err != nil {
			report.Checks = append(report.Checks, Check{Name: "runtime-version", Status: "fail", Detail: err.Error()})
		} else if version, err := codexruntime.ParseSemanticVersion(rawVersion); err != nil {
			report.Checks = append(report.Checks, Check{Name: "runtime-version", Status: "fail", Detail: err.Error()})
		} else {
			report.RuntimeVersion = version
			if report.Evidence != nil && version != report.Evidence.CodexVersion {
				report.Checks = append(report.Checks, Check{
					Name:   "runtime-version",
					Status: "warn",
					Detail: fmt.Sprintf("runtime %s differs from pinned interactive evidence %s; read-only account/quota queries may proceed only through strict response validation, while interactive/login/logout operations remain pinned", version, report.Evidence.CodexVersion),
				})
			} else {
				report.Checks = append(report.Checks, Check{Name: "runtime-version", Status: "ok", Detail: version})
			}
		}
	} else {
		report.Checks = append(report.Checks, Check{
			Name:   "runtime-version",
			Status: "skip",
			Detail: "not probed; use doctor --probe-runtime to execute only codex --version in an isolated unauthenticated CODEX_HOME",
		})
	}

	profiles, _, err := store.List()
	if err != nil {
		report.Checks = append(report.Checks, Check{Name: "registry", Status: "fail", Detail: err.Error()})
		return report
	}
	report.Checks = append(report.Checks, Check{Name: "registry", Status: "ok", Detail: fmt.Sprintf("%d registered profiles", len(profiles))})
	for _, p := range profiles {
		report.Checks = append(report.Checks, checkProfile(store, p)...)
	}

	if goruntime.GOOS == "windows" {
		report.Checks = append(report.Checks, Check{
			Name:   "windows-locking",
			Status: "fail",
			Detail: "secure Windows profile locking is not implemented in this preview; do not use this build for authenticated profiles",
		})
	}
	return report
}

func checkBinary(rt *codexruntime.Runtime) Check {
	if err := rt.BinaryAvailabilityError(); err != nil {
		return Check{Name: "codex-binary", Status: "fail", Detail: err.Error()}
	}
	path := rt.Binary
	info, err := os.Lstat(path)
	if err != nil {
		return Check{Name: "codex-binary", Status: "fail", Detail: err.Error()}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Check{Name: "codex-binary", Status: "warn", Detail: "configured Codex binary is a symlink; pin an audited absolute binary for release validation"}
	}
	if !info.Mode().IsRegular() {
		return Check{Name: "codex-binary", Status: "fail", Detail: "configured Codex binary is not a regular file"}
	}
	return Check{Name: "codex-binary", Status: "ok", Detail: path}
}

func checkProfile(store *profile.Store, p profile.Profile) []Check {
	prefix := "profile:" + p.Alias
	root, err := store.ProfileRoot(p)
	if err != nil {
		return []Check{{Name: prefix, Status: "fail", Detail: err.Error()}}
	}
	checks := []Check{{Name: prefix + ":path", Status: "ok", Detail: root}}
	homeInfo, err := os.Lstat(p.CodexHome)
	if err != nil {
		checks = append(checks, Check{Name: prefix + ":home", Status: "fail", Detail: err.Error()})
	} else if homeInfo.Mode()&os.ModeSymlink != 0 || !homeInfo.IsDir() {
		checks = append(checks, Check{Name: prefix + ":home", Status: "fail", Detail: "CODEX_HOME must be a real directory"})
	} else if goruntime.GOOS != "windows" && homeInfo.Mode().Perm()&0o077 != 0 {
		checks = append(checks, Check{Name: prefix + ":home", Status: "fail", Detail: fmt.Sprintf("permissions too broad: %o", homeInfo.Mode().Perm())})
	} else {
		checks = append(checks, Check{Name: prefix + ":home", Status: "ok"})
	}

	configPath := filepath.Join(p.CodexHome, "config.toml")
	if err := securefs.EnsurePrivateFile(configPath); err != nil {
		checks = append(checks, Check{Name: prefix + ":config", Status: "fail", Detail: err.Error()})
	} else if info, err := os.Lstat(configPath); err != nil {
		checks = append(checks, Check{Name: prefix + ":config", Status: "fail", Detail: err.Error()})
	} else if !info.Mode().IsRegular() {
		checks = append(checks, Check{Name: prefix + ":config", Status: "fail", Detail: "config.toml must be a regular file"})
	} else {
		checks = append(checks, Check{Name: prefix + ":config", Status: "ok"})
	}
	return checks
}

func loadEvidence(path string) (Evidence, error) {
	if strings.TrimSpace(path) == "" {
		return BuiltInEvidence, nil
	}
	if err := securefs.EnsurePrivateFile(path); err != nil {
		// Repository evidence is intentionally not under the private CodexPool root.
		// A regular read-only repository file may have normal source permissions.
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return Evidence{}, err
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Evidence{}, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(b))
	if digest != EvidenceManifestSHA256 {
		return Evidence{}, fmt.Errorf("evidence manifest hash mismatch: got %s", digest)
	}
	var evidence Evidence
	if err := json.Unmarshal(b, &evidence); err != nil {
		return Evidence{}, fmt.Errorf("decode evidence manifest: %w", err)
	}
	if evidence.CodexVersion == "" || evidence.ResearchDate == "" {
		return Evidence{}, errors.New("evidence manifest is missing required version metadata")
	}
	return evidence, nil
}
