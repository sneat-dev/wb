package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// newHostLevelGitFixture is newGitFixtureAtRepository's central-store,
// hosted-origin counterpart: a canonical clone whose origin names a real
// forge, so the default central store mode (no
// ~/.config/wb/worktrees.yaml, exactly the field report in #594) derives the
// literal host level and places checkouts at
// <projects-root>/.worktrees/<task>/<host>/<owner>/<repository>.
func newHostLevelGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	home := filepath.Join(projectsRoot, ".wb")
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	// Central is the default store mode; an isolated, empty config
	// directory keeps this hermetic against any ambient user config that
	// would select another mode.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	remote := newSharedHostedOrigin(t)
	canonical := cloneHostedOriginInto(t, remote, projectsRoot, "acme", "app")
	configureGitUser(t, canonical)
	var err error
	projectsRoot, err = filepath.EvalSymlinks(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		t.Fatal(err)
	}
	homeParent, err := filepath.EvalSymlinks(filepath.Dir(home))
	if err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(homeParent, filepath.Base(home))
	return &gitFixture{projectsRoot: projectsRoot, canonical: canonical, remote: remote, home: home}
}

// prepareMergedHostLevelTask is prepareMergedTask's host-level counterpart:
// one task, one repository, created, committed to, pushed, and merged
// directly into the canonical clone's main — the same "direct push, no pull
// request" integration path TestCleanupAcceptsExactDirectPushIntegrationWithoutPullRequest
// already exercises for the legacy layout, now proved end to end at the
// 3-segment <task>/<host>/<owner>/<repository> layout `wb worktree create`
// actually writes by default.
func prepareMergedHostLevelTask(t *testing.T, task string) (*gitFixture, CreateResult, time.Time) {
	t.Helper()
	fixture := newHostLevelGitFixture(t)
	result, _, mergedAt := prepareMergedTaskInFixture(t, fixture, task)
	return fixture, result, mergedAt
}

// TestCleanupEndToEndCreateThenApplyRetiresHostLevelWorktree is the exact
// reproduction from sneat-dev/wb#594: `wb worktree create` places the
// checkout at the default central-store host-level layout, and `wb worktree
// cleanup <task> --apply` must retire it — including the now-empty
// <owner> and <host> ancestor directories — rather than refusing it as an
// "unsupported hierarchy".
func TestCleanupEndToEndCreateThenApplyRetiresHostLevelWorktree(t *testing.T) {
	const task = "cleanup-host-level-e2e"
	fixture, result, mergedAt := prepareMergedHostLevelTask(t, task)
	// No pull request exists for this landing; the fake `gh` only needs to
	// answer honestly that none is found.
	installMergedPullRequestFixtures(t, nil, time.Time{})

	wantWorktree := filepath.Join(fixture.projectsRoot, ".worktrees", task, "github.com", "acme", "app")
	if result.WorktreeDir != wantWorktree {
		t.Fatalf("worktree create did not place the checkout at the host-level layout the bug reported: got %q, want %q", result.WorktreeDir, wantWorktree)
	}

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         task,
		Base:         "main",
		Apply:        true,
		Now:          func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("cleanup did not apply to the host-level checkout: %#v", outcome.Results)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("host-level worktree survived cleanup --apply: %v", statErr)
	}
	ownerDir := filepath.Dir(result.WorktreeDir)
	hostDir := filepath.Dir(ownerDir)
	if _, statErr := os.Stat(ownerDir); !os.IsNotExist(statErr) {
		t.Fatalf("empty owner directory %s was not retired: %v", ownerDir, statErr)
	}
	if _, statErr := os.Stat(hostDir); !os.IsNotExist(statErr) {
		t.Fatalf("empty host directory %s was not retired: %v", hostDir, statErr)
	}
}

// TestGCApplyRetiresAMergedHostLevelWorktree proves `wb worktree gc --apply`
// — the remedy #594 itself named as failing — also resolves the host-level
// layout, since GC's own removal path is Cleanup underneath.
func TestGCApplyRetiresAMergedHostLevelWorktree(t *testing.T) {
	const task = "gc-host-level-e2e"
	fixture, result, mergedAt := prepareMergedHostLevelTask(t, task)
	installMergedPullRequestFixtures(t, nil, time.Time{})

	outcome, err := GC(context.Background(), GCOptions{
		// The fixture's checkout was created moments ago; the in-use rule is
		// not what this test is about.
		SessionFreshness: DisableSessionFreshness,
		ProjectsRoot:     fixture.projectsRoot, Tasks: []string{task}, Apply: true,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryFor(t, outcome, task)
	if !entry.Applied {
		t.Fatalf("gc --apply did not retire the host-level worktree: %#v", entry)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("host-level worktree survived gc --apply: %v", statErr)
	}
	ownerDir := filepath.Dir(result.WorktreeDir)
	hostDir := filepath.Dir(ownerDir)
	if _, statErr := os.Stat(ownerDir); !os.IsNotExist(statErr) {
		t.Fatalf("empty owner directory %s was not retired by gc: %v", ownerDir, statErr)
	}
	if _, statErr := os.Stat(hostDir); !os.IsNotExist(statErr) {
		t.Fatalf("empty host directory %s was not retired by gc: %v", hostDir, statErr)
	}
}
