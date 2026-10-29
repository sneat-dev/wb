//go:build e2e

package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// landIgnoreRules pushes a target commit that ignores profile.cov anywhere
// (root .gitignore) and *.tmp under sub/ (a nested .gitignore the canonical
// clone has never had), returning the canonical HEAD before and the landed
// head.
func landIgnoreRules(t *testing.T) (engineFixture, string, string) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	before := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	updater := filepath.Join(t.TempDir(), "updater")
	runEngineGit(t, filepath.Dir(updater), "clone", f.repository.CloneURL, updater)
	runEngineGit(t, updater, "config", "user.name", "WB Test")
	runEngineGit(t, updater, "config", "user.email", "wb@example.test")
	writeEngineFile(t, filepath.Join(updater, ".gitignore"), "**/profile.cov\n")
	writeEngineFile(t, filepath.Join(updater, "sub", ".gitignore"), "*.tmp\n")
	runEngineGit(t, updater, "add", "-A")
	runEngineGit(t, updater, "commit", "-m", "ignore coverage profiles")
	after := strings.TrimSpace(runEngineGit(t, updater, "rev-parse", "HEAD"))
	runEngineGit(t, updater, "push", "origin", "main")
	return f, before, after
}

func TestE2ECanonicalSyncFastForwardsOverProfileTheLandingIgnores(t *testing.T) {
	t.Parallel()
	f, before, after := landIgnoreRules(t)
	writeEngineFile(t, filepath.Join(f.canonical, "profile.cov"), "mode: set\n")
	writeEngineFile(t, filepath.Join(f.canonical, "sub", "scratch.tmp"), "scratch\n")
	if status := runEngineGit(t, f.canonical, "status", "--porcelain=v1"); !strings.Contains(status, "?? profile.cov") {
		t.Fatalf("precondition: profile.cov not dirty before landing: %q", status)
	}
	var updates []CheckoutUpdate
	status, err := syncCanonicalMergeTarget(t.Context(), f.canonical, "main", after, 0, 0, func(_ context.Context, u CheckoutUpdate) { updates = append(updates, u) })
	if err != nil || status != "fast_forwarded" {
		t.Fatalf("sync=%q %v", status, err)
	}
	head := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	if head != after || len(updates) != 1 || updates[0].OldSHA != before || updates[0].NewSHA != after {
		t.Fatalf("head=%s updates=%+v", head, updates)
	}
	if dirty := runEngineGit(t, f.canonical, "status", "--porcelain=v1"); strings.TrimSpace(dirty) != "" {
		t.Fatalf("canonical not clean after sync: %q", dirty)
	}
	if content, err := os.ReadFile(filepath.Join(f.canonical, "profile.cov")); err != nil || string(content) != "mode: set\n" {
		t.Fatalf("ignored profile altered: %q %v", content, err)
	}
}

func TestE2ECanonicalSyncStillBlocksUntrackedFileTheLandingDoesNotIgnore(t *testing.T) {
	t.Parallel()
	f, before, after := landIgnoreRules(t)
	writeEngineFile(t, filepath.Join(f.canonical, "profile.cov"), "mode: set\n")
	writeEngineFile(t, filepath.Join(f.canonical, "notes.txt"), "unlanded work\n")
	status, err := syncCanonicalMergeTarget(t.Context(), f.canonical, "main", after, 0, 0, nil)
	if status != "blocked_dirty" || err == nil || !strings.Contains(err.Error(), "not ignored by refs/remotes/origin/main: notes.txt") {
		t.Fatalf("sync=%q %v", status, err)
	}
	if head := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD")); head != before {
		t.Fatalf("blocked sync moved HEAD to %s", head)
	}
	fetched := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "refs/remotes/origin/main"))
	if fetched != after {
		t.Fatalf("target not fetched before judging dirt: %s", fetched)
	}
}

func TestE2ECanonicalSyncTreatsLeadingColonPathsLiterally(t *testing.T) {
	t.Parallel()
	f, before, after := landIgnoreRules(t)
	writeEngineFile(t, filepath.Join(f.canonical, ":profile.cov"), "pathspec magic lookalike\n")
	status, err := syncCanonicalMergeTarget(t.Context(), f.canonical, "main", after, 0, 0, nil)
	if status != "blocked_dirty" || err == nil || !strings.Contains(err.Error(), "not ignored by refs/remotes/origin/main: :profile.cov") {
		t.Fatalf("sync=%q %v", status, err)
	}
	if head := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD")); head != before {
		t.Fatalf("blocked sync moved HEAD to %s", head)
	}
}
