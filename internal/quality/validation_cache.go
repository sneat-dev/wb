package quality

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const validationCacheSchema = 1

// ValidationCacheKey identifies the exact inputs that make a verification
// report reusable. Checks remain ordered because the order is part of the
// command contract and can affect the resulting evidence.
type ValidationCacheKey struct {
	Repository       string   `json:"repository"`
	TargetRevision   string   `json:"target_revision"`
	Checks           []Check  `json:"checks"`
	QualityConfigSHA string   `json:"quality_config_sha"`
	WBRevision       string   `json:"wb_revision"`
	GoToolchain      string   `json:"go_toolchain"`
	ModuleFiles      []string `json:"module_files"`
}

type validationCacheRecord struct {
	Schema int                `json:"schema"`
	Key    ValidationCacheKey `json:"key"`
	Report VerificationReport `json:"report"`
	Digest string             `json:"digest"`
}

// NewValidationCacheKey fingerprints repository-local policy and module
// manifests. The caller supplies the exact target revision and WB revision.
func NewValidationCacheKey(repository, targetRevision, root, wbRevision string, checks []Check) (ValidationCacheKey, error) {
	key := ValidationCacheKey{
		Repository: repository, TargetRevision: targetRevision,
		Checks: append([]Check(nil), checks...), WBRevision: wbRevision,
		GoToolchain: runtime.Version(),
	}
	for _, name := range []string{repositoryQualityConfigPath} {
		path := filepath.Join(root, name)
		if err := addValidationCacheFile(&key.QualityConfigSHA, path); err != nil {
			return ValidationCacheKey{}, err
		}
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == ".git" || entry.Name() == "vendor" || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		base := entry.Name()
		if base == "go.mod" || base == "go.sum" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return ValidationCacheKey{}, err
	}
	sort.Strings(files)
	for _, path := range files {
		digest, err := fileDigest(path)
		if err != nil {
			return ValidationCacheKey{}, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return ValidationCacheKey{}, err
		}
		key.ModuleFiles = append(key.ModuleFiles, filepath.ToSlash(rel)+"="+digest)
	}
	return key, nil
}

func addValidationCacheFile(dst *string, path string) error {
	digest, err := fileDigest(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	*dst = digest
	return nil
}

func fileDigest(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func validationCacheDigest(schema int, key ValidationCacheKey, report VerificationReport) (string, error) {
	payload, err := json.Marshal(struct {
		Schema int                `json:"schema"`
		Key    ValidationCacheKey `json:"key"`
		Report VerificationReport `json:"report"`
	}{schema, key, report})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func validationCacheKeyDigest(key ValidationCacheKey) (string, error) {
	payload, err := json.Marshal(struct {
		Schema int                `json:"schema"`
		Key    ValidationCacheKey `json:"key"`
	}{validationCacheSchema, key})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// LoadValidationCache returns only an intact terminal report with an exact key.
// Any malformed, stale, or otherwise incomplete record is a cache miss.
func LoadValidationCache(cacheRoot string, key ValidationCacheKey) (VerificationReport, bool, error) {
	digest, err := validationCacheKeyDigest(key)
	if err != nil {
		return VerificationReport{}, false, err
	}
	path := filepath.Join(cacheRoot, digest+".json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return VerificationReport{}, false, nil
	}
	if err != nil {
		return VerificationReport{}, false, err
	}
	var record validationCacheRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return VerificationReport{}, false, nil
	}
	if record.Schema != validationCacheSchema || !sameValidationCacheKey(record.Key, key) || record.Report.Status == StatusSkipped || record.Report.Revision != key.TargetRevision || !record.Report.WorkspaceClean {
		return VerificationReport{}, false, nil
	}
	actual, err := validationCacheDigest(record.Schema, record.Key, record.Report)
	if err != nil || actual != record.Digest {
		return VerificationReport{}, false, nil
	}
	return record.Report, true, nil
}

func sameValidationCacheKey(a, b ValidationCacheKey) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}

// SaveValidationCache writes terminal evidence atomically. Failed writes are
// returned to the caller; a cache failure never changes validation semantics.
func SaveValidationCache(cacheRoot string, key ValidationCacheKey, report VerificationReport) error {
	if report.Status == StatusSkipped || report.Revision != key.TargetRevision || !report.WorkspaceClean {
		return fmt.Errorf("validation cache accepts only terminal clean evidence")
	}
	keyDigest, err := validationCacheKeyDigest(key)
	if err != nil {
		return err
	}
	digest, err := validationCacheDigest(validationCacheSchema, key, report)
	if err != nil {
		return err
	}
	record := validationCacheRecord{Schema: validationCacheSchema, Key: key, Report: report, Digest: digest}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(cacheRoot, ".validation-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(cacheRoot, keyDigest+".json"))
}

// ValidationCacheDir is kept in one place so all merge baseline callers share
// the same private WB state and tests can replace it without touching a user
// repository.
func ValidationCacheDir(root string) string {
	if override := strings.TrimSpace(os.Getenv("WB_VALIDATION_CACHE")); override != "" {
		return override
	}
	return filepath.Join(root, "cache", "worktree-merge-validation")
}
