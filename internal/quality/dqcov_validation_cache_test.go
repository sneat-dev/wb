package quality

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDqCovAddValidationCacheFileCoversAllOutcomes pins the three ways a policy
// file can participate in a cache key: absent is neutral, present is digested,
// and unreadable fails closed.
func TestDqCovAddValidationCacheFileCoversAllOutcomes(t *testing.T) {
	root := t.TempDir()

	unchanged := ""
	if err := addValidationCacheFile(&unchanged, filepath.Join(root, "absent.yaml")); err != nil {
		t.Fatalf("absent file error = %v, want nil", err)
	}
	if unchanged != "" {
		t.Fatalf("absent file digest = %q, want the destination untouched", unchanged)
	}

	path := filepath.Join(root, "quality.yaml")
	writeQualityFile(t, path, "version: 1\n")
	digest := ""
	if err := addValidationCacheFile(&digest, path); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("version: 1\n"))
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %q, want the file's sha256", digest)
	}

	directory := filepath.Join(root, "as-directory.yaml")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := addValidationCacheFile(&digest, directory); err == nil {
		t.Fatal("a directory policy path was accepted")
	}
}

func TestDqCovValidationCacheDirHonorsOverride(t *testing.T) {
	root := t.TempDir()
	if got, want := ValidationCacheDir(root), filepath.Join(root, "cache", "worktree-merge-validation"); got != want {
		t.Fatalf("ValidationCacheDir = %q, want %q", got, want)
	}
	override := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv("WB_VALIDATION_CACHE", "  "+override+"  ")
	if got := ValidationCacheDir(root); got != override {
		t.Fatalf("ValidationCacheDir with override = %q, want %q", got, override)
	}
	t.Setenv("WB_VALIDATION_CACHE", "   ")
	if got, want := ValidationCacheDir(root), filepath.Join(root, "cache", "worktree-merge-validation"); got != want {
		t.Fatalf("blank override = %q, want the default %q", got, want)
	}
}

// TestDqCovNewValidationCacheKeyFingerprintsPolicyAndModules asserts the key
// binds repository policy, every module manifest, the toolchain, and the exact
// ordered check list, while pruning generated dependency trees.
func TestDqCovNewValidationCacheKeyFingerprintsPolicyAndModules(t *testing.T) {
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, repositoryQualityConfigPath), "version: 1\n")
	writeQualityFile(t, filepath.Join(root, "go.mod"), "module example.test/key\n\ngo 1.24\n")
	writeQualityFile(t, filepath.Join(root, "go.sum"), "example.test/dep v1.0.0 h1:test\n")
	writeQualityFile(t, filepath.Join(root, "sub", "go.mod"), "module example.test/sub\n\ngo 1.24\n")
	for _, pruned := range []string{".git", "vendor", "node_modules"} {
		writeQualityFile(t, filepath.Join(root, pruned, "go.mod"), "module example.test/pruned\n")
	}
	checks := []Check{CheckTest, CheckLint}
	key, err := NewValidationCacheKey("example/key", "revision-1", root, "wb-revision", checks)
	if err != nil {
		t.Fatal(err)
	}
	if key.Repository != "example/key" || key.TargetRevision != "revision-1" || key.WBRevision != "wb-revision" || key.GoToolchain == "" {
		t.Fatalf("key = %+v, want caller identity and toolchain", key)
	}
	if strings.Join(checkStrings(key.Checks), ",") != "test,lint" {
		t.Fatalf("checks = %v, want the caller's order preserved", key.Checks)
	}
	configSum := sha256.Sum256([]byte("version: 1\n"))
	if key.QualityConfigSHA != hex.EncodeToString(configSum[:]) {
		t.Fatalf("QualityConfigSHA = %q, want the policy digest", key.QualityConfigSHA)
	}
	moduleSum := sha256.Sum256([]byte("module example.test/key\n\ngo 1.24\n"))
	subSum := sha256.Sum256([]byte("module example.test/sub\n\ngo 1.24\n"))
	sumSum := sha256.Sum256([]byte("example.test/dep v1.0.0 h1:test\n"))
	want := []string{"go.mod=" + hex.EncodeToString(moduleSum[:]), "go.sum=" + hex.EncodeToString(sumSum[:]), "sub/go.mod=" + hex.EncodeToString(subSum[:])}
	if strings.Join(key.ModuleFiles, "|") != strings.Join(want, "|") {
		t.Fatalf("ModuleFiles = %v, want %v", key.ModuleFiles, want)
	}
	for _, entry := range key.ModuleFiles {
		if strings.Contains(entry, "pruned") {
			t.Fatalf("generated dependency tree entered the key: %v", key.ModuleFiles)
		}
	}
}

// TestDqCovNewValidationCacheKeyFailsClosed covers each unreadable input: an
// unreadable policy path, an unreadable subtree, and an unreadable module
// manifest.
func TestDqCovNewValidationCacheKeyFailsClosed(t *testing.T) {
	t.Run("policy path is a directory", func(t *testing.T) {
		root := t.TempDir()
		writeQualityFile(t, filepath.Join(root, "go.mod"), "module example.test/key\n")
		if err := os.MkdirAll(filepath.Join(root, repositoryQualityConfigPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := NewValidationCacheKey("example/key", "rev", root, "wb", nil); err == nil {
			t.Fatal("an unreadable policy path was accepted")
		}
	})

	t.Run("unreadable subtree", func(t *testing.T) {
		root := t.TempDir()
		writeQualityFile(t, filepath.Join(root, "go.mod"), "module example.test/key\n")
		denied := filepath.Join(root, "denied")
		if err := os.Mkdir(denied, 0o755); err != nil {
			t.Fatal(err)
		}
		dqCovChmod(t, denied, 0)
		if _, err := NewValidationCacheKey("example/key", "rev", root, "wb", nil); err == nil {
			t.Fatal("an unreadable subtree was accepted")
		}
	})

	t.Run("dangling module manifest", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(filepath.Join(root, "absent-go.mod"), filepath.Join(root, "go.mod")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := NewValidationCacheKey("example/key", "rev", root, "wb", nil); err == nil {
			t.Fatal("a dangling module manifest was accepted")
		}
	})
}

// dqCovSaveValidCache writes one internally consistent record and returns the
// key it used.
func dqCovSaveValidCache(t *testing.T, cacheRoot string) ValidationCacheKey {
	t.Helper()
	key := ValidationCacheKey{Repository: "example/cache", TargetRevision: "revision-1", GoToolchain: "go1.24"}
	report := VerificationReport{Repository: key.Repository, Revision: key.TargetRevision, WorkspaceClean: true, Status: StatusPassed}
	if err := SaveValidationCache(cacheRoot, key, report); err != nil {
		t.Fatal(err)
	}
	return key
}

// TestDqCovLoadValidationCacheMissesEveryWeakenedRecord proves the loader only
// returns evidence that is intact, current, terminal, and clean.
func TestDqCovLoadValidationCacheMissesEveryWeakenedRecord(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	key := dqCovSaveValidCache(t, cacheRoot)

	report, hit, err := LoadValidationCache(cacheRoot, key)
	if err != nil || !hit || report.Status != StatusPassed {
		t.Fatalf("load = %+v hit=%v err=%v, want the saved evidence", report, hit, err)
	}

	entries, err := os.ReadDir(cacheRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache entries = %v err=%v", entries, err)
	}
	recordPath := filepath.Join(cacheRoot, entries[0].Name())
	validRaw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var valid map[string]any
	if err := json.Unmarshal(validRaw, &valid); err != nil {
		t.Fatal(err)
	}

	write := func(mutate func(record map[string]any)) {
		t.Helper()
		raw, err := json.Marshal(valid)
		if err != nil {
			t.Fatal(err)
		}
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		mutate(record)
		raw, err = json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(recordPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, hit, err := LoadValidationCache(cacheRoot, key); err != nil || hit {
			t.Fatalf("weakened record = hit=%v err=%v, want a miss", hit, err)
		}
	}

	t.Run("invalid json", func(t *testing.T) {
		if err := os.WriteFile(recordPath, []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, hit, err := LoadValidationCache(cacheRoot, key); err != nil || hit {
			t.Fatalf("invalid json = hit=%v err=%v, want a miss", hit, err)
		}
	})

	write(func(record map[string]any) { record["schema"] = float64(2) })
	write(func(record map[string]any) {
		record["key"].(map[string]any)["repository"] = "example/other"
	})
	write(func(record map[string]any) {
		record["report"].(map[string]any)["revision"] = "revision-2"
	})
	write(func(record map[string]any) {
		record["report"].(map[string]any)["workspace_clean"] = false
	})
	write(func(record map[string]any) {
		record["report"].(map[string]any)["status"] = string(StatusSkipped)
	})
	write(func(record map[string]any) { record["digest"] = "corrupted" })

	if err := os.WriteFile(recordPath, validRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := LoadValidationCache(cacheRoot, key); err != nil || !hit {
		t.Fatalf("restored record = hit=%v err=%v, want the original evidence", hit, err)
	}
}

// TestDqCovLoadValidationCacheSurfacesUnreadableCacheRoot distinguishes a
// missing record (a miss) from an unreadable cache root (an error).
func TestDqCovLoadValidationCacheSurfacesUnreadableCacheRoot(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "cache-is-a-file")
	writeQualityFile(t, blocker, "not a directory")
	key := ValidationCacheKey{Repository: "example/cache", TargetRevision: "rev"}
	if _, hit, err := LoadValidationCache(blocker, key); err == nil || hit {
		t.Fatalf("load err=%v hit=%v, want an unreadable-root error", err, hit)
	}
}

// TestDqCovSaveValidationCacheRejectsNonTerminalEvidence pins that the cache
// never stores evidence that is not a terminal, clean, current success.
func TestDqCovSaveValidationCacheRejectsNonTerminalEvidence(t *testing.T) {
	key := ValidationCacheKey{Repository: "example/cache", TargetRevision: "revision-1"}
	for _, tc := range []struct {
		name   string
		report VerificationReport
	}{
		{name: "skipped", report: VerificationReport{Repository: key.Repository, Revision: key.TargetRevision, WorkspaceClean: true, Status: StatusSkipped}},
		{name: "stale revision", report: VerificationReport{Repository: key.Repository, Revision: "revision-2", WorkspaceClean: true, Status: StatusPassed}},
		{name: "dirty workspace", report: VerificationReport{Repository: key.Repository, Revision: key.TargetRevision, Status: StatusPassed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cacheRoot := filepath.Join(t.TempDir(), "cache")
			err := SaveValidationCache(cacheRoot, key, tc.report)
			if err == nil || !strings.Contains(err.Error(), "terminal clean evidence") {
				t.Fatalf("error = %v, want the terminal-evidence requirement", err)
			}
			if entries, readErr := os.ReadDir(cacheRoot); readErr == nil && len(entries) != 0 {
				t.Fatalf("rejected evidence was written: %v", entries)
			}
		})
	}
}

// TestDqCovSaveValidationCacheSurfacesFilesystemFailures covers an unusable
// cache root and a cache root that refuses new files.
func TestDqCovSaveValidationCacheSurfacesFilesystemFailures(t *testing.T) {
	key := ValidationCacheKey{Repository: "example/cache", TargetRevision: "revision-1"}
	report := VerificationReport{Repository: key.Repository, Revision: key.TargetRevision, WorkspaceClean: true, Status: StatusPassed}

	t.Run("cache root parent is a file", func(t *testing.T) {
		root := t.TempDir()
		blocker := filepath.Join(root, "blocker")
		writeQualityFile(t, blocker, "x")
		if err := SaveValidationCache(filepath.Join(blocker, "cache"), key, report); err == nil {
			t.Fatal("an impossible cache root was accepted")
		}
	})

	t.Run("read-only cache root", func(t *testing.T) {
		cacheRoot := filepath.Join(t.TempDir(), "cache")
		if err := os.Mkdir(cacheRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		dqCovChmod(t, cacheRoot, 0o500)
		if err := SaveValidationCache(cacheRoot, key, report); err == nil {
			t.Fatal("a read-only cache root was accepted")
		}
	})
}
