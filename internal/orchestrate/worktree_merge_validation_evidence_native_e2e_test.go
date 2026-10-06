//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestE2EWorktreeMergeTargetEvidenceCachesExactArchiveAfterCleanup(t *testing.T) {
	t.Parallel()
	// The cache-directory owner supports an environment override. This fixture
	// requires the native home boundary to own its cache, without mutating it.
	if strings.TrimSpace(os.Getenv("WB_VALIDATION_CACHE")) != "" {
		t.Fatal("native home-owned cache fixture requires WB_VALIDATION_CACHE unset")
	}
	fixture := newExplicitRootEngineFixture(t)
	witness := filepath.Join(t.TempDir(), "snapshot-path")
	writeEngineGoModule(t, fixture.canonical, "package app\n\nfunc Value() int { return 1 }\n")
	writeEngineFile(t, filepath.Join(fixture.canonical, "app_test.go"), fmt.Sprintf(`package app
import ("os"; "testing")
func TestExactTarget(t *testing.T) {
 if Value()!=1 { t.Fatal("target changed") }
 path,err:=os.Getwd(); if err!=nil {t.Fatal(err)}
 if err:=os.WriteFile(%q,[]byte(path),0600);err!=nil {t.Fatal(err)}
}
`, witness))
	runEngineGit(t, fixture.canonical, "add", "go.mod", "app.go", "app_test.go")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: exact target evidence")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	// This mutable candidate cannot pass the quality check. Only the target
	// archive can execute the test that records its own transient path.
	writeEngineFile(t, filepath.Join(fixture.canonical, "app.go"), "package app\nfunc Value() int { return undefinedCandidate }\n")
	cacheHome := t.TempDir()
	deps := nativeWorktreeMergeBaselineDependencies()
	deps.home = func() (string, error) { return cacheHome, nil }
	first, err := verifyWorktreeMergeTargetChecksWithDependencies(context.Background(), fixture.repository.Slug, fixture.canonical, target, time.Minute, 0, time.Minute, 0, []quality.Check{quality.CheckTest}, deps)
	if err != nil || first.Status != quality.StatusPassed || first.Path != "git:"+target || first.Revision != target || !first.WorkspaceClean {
		t.Fatalf("native target report %+v, err %v", first, err)
	}
	raw, err := os.ReadFile(witness)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := string(raw)
	if snapshot == "" || snapshot == fixture.canonical || !strings.HasPrefix(filepath.Base(filepath.Dir(snapshot)), "wb-worktree-merge-target-") {
		t.Fatalf("test ran outside target archive: %q", snapshot)
	}
	if _, err := os.Stat(snapshot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target snapshot survived return: %s, %v", snapshot, err)
	}
	cacheDir := quality.ValidationCacheDir(filepath.Join(cacheHome, ".wb"))
	entries, err := os.ReadDir(cacheDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("native disk cache %s: %v, %v", cacheDir, entries, err)
	}
	if err := os.WriteFile(witness, []byte("cache must avoid another test execution"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := verifyWorktreeMergeTargetChecksWithDependencies(context.Background(), fixture.repository.Slug, fixture.canonical, target, time.Minute, 0, time.Minute, 0, []quality.Check{quality.CheckTest}, deps)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("native cache reuse first %+v, second %+v, err %v", first, second, err)
	}
	raw, err = os.ReadFile(witness)
	if err != nil || string(raw) != "cache must avoid another test execution" {
		t.Fatalf("cached baseline executed checks again: %q, %v", raw, err)
	}
}
