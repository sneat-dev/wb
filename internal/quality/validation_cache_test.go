package quality

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidationCacheReusesOnlyIntactExactEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	write := func(name, contents string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/cache\n\ngo 1.26\n")
	write("go.sum", "example.test/dep v1.0.0 h1:test\n")
	checks := []Check{CheckLint, CheckTest, CheckBuild, CheckSpec}
	key, err := NewValidationCacheKey("example/cache", "0123456789012345678901234567890123456789", root, "wb-revision", checks, nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	report := VerificationReport{Repository: "example/cache", Path: "git:" + key.TargetRevision, Revision: key.TargetRevision, WorkspaceClean: true, Status: StatusPassed, Results: []VerificationEntry{{Language: "go", Check: CheckBuild, Status: StatusPassed}}}
	if err := SaveValidationCache(cache, key, report); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadValidationCache(cache, key)
	if err != nil || !ok || got.Status != StatusPassed || got.Revision != report.Revision {
		t.Fatalf("cache load = %+v, hit=%v, err=%v", got, ok, err)
	}

	key.Checks[0], key.Checks[1] = key.Checks[1], key.Checks[0]
	if _, ok, err := LoadValidationCache(cache, key); err != nil || ok {
		t.Fatalf("ordered check mismatch = hit=%v err=%v", ok, err)
	}

	key.Checks[0], key.Checks[1] = key.Checks[1], key.Checks[0]
	files, err := os.ReadDir(cache)
	if err != nil || len(files) != 1 {
		t.Fatalf("cache files = %v err=%v", files, err)
	}
	path := filepath.Join(cache, files[0].Name())
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record["digest"] = "corrupted"
	raw, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadValidationCache(cache, key); err != nil || ok {
		t.Fatalf("corrupt cache = hit=%v err=%v", ok, err)
	}
}

func TestValidationCacheInvalidatesChangedExecutionLimits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := t.TempDir()
	base := RunOptions{Timeout: 20 * time.Minute, Retry: 1, CheckTimeout: 25 * time.Minute, ShardAttemptTimeout: 10 * time.Minute}
	makeKey := func(options RunOptions) ValidationCacheKey {
		t.Helper()
		key, err := NewValidationCacheKey("example/cache", "target-revision", root, "wb-revision", []Check{CheckLint, CheckTest}, nil, options)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	key := makeKey(base)
	if err := SaveValidationCache(cache, key, VerificationReport{Repository: key.Repository, Revision: key.TargetRevision, WorkspaceClean: true, Status: StatusFailed}); err != nil {
		t.Fatal(err)
	}
	changed := []RunOptions{
		{Timeout: 35 * time.Minute, Retry: base.Retry, CheckTimeout: base.CheckTimeout, ShardAttemptTimeout: base.ShardAttemptTimeout},
		{Timeout: base.Timeout, Retry: 2, CheckTimeout: base.CheckTimeout, ShardAttemptTimeout: base.ShardAttemptTimeout},
		{Timeout: base.Timeout, Retry: base.Retry, CheckTimeout: 35 * time.Minute, ShardAttemptTimeout: base.ShardAttemptTimeout},
		{Timeout: base.Timeout, Retry: base.Retry, CheckTimeout: base.CheckTimeout, ShardAttemptTimeout: 15 * time.Minute},
	}
	for _, options := range changed {
		if _, hit, err := LoadValidationCache(cache, makeKey(options)); err != nil || hit {
			t.Fatalf("cache reused changed execution limits %+v: hit=%v err=%v", options, hit, err)
		}
	}
	if _, hit, err := LoadValidationCache(cache, key); err != nil || !hit {
		t.Fatalf("unchanged limits missed cache: hit=%v err=%v", hit, err)
	}
}

func TestValidationCacheRejectsEvidenceFromDifferentValidator(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/cache\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checks := []Check{CheckSpec}
	key, err := NewValidationCacheKey("example/cache", "0123456789012345678901234567890123456789", root, "wb-revision", checks, nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	key.ValidatorSHAs = map[string]string{"specscore": "validator-v1"}
	if err := SaveValidationCache(cache, key, VerificationReport{Repository: key.Repository, Revision: key.TargetRevision, WorkspaceClean: true, Status: StatusPassed}); err != nil {
		t.Fatal(err)
	}

	key.ValidatorSHAs = map[string]string{"specscore": "validator-v2"}
	if _, ok, err := LoadValidationCache(cache, key); err != nil || ok {
		t.Fatalf("changed validator cache = hit=%v err=%v, want miss", ok, err)
	}
}
