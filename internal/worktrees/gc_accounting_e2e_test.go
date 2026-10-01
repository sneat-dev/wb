//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // the Git fixture and gh index configure process-wide environment
func TestE2EGCApplyAccountsForRetiredAndLeftRepositories(t *testing.T) {
	const task = "gc-accounting"
	fixture, created, head, landed, mergedAt := prepareAbsorbedCandidate(t, task)
	installPerCommitPullRequestFixture(t, map[string]string{
		head: mergedPullRequestPayload(t, 77, strings.Repeat("a", 40), landed, mergedAt),
	})
	options := GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{task}, SkipSizes: true,
		SessionFreshness: DisableSessionFreshness, Now: func() time.Time { return mergedAt.Add(time.Hour) },
	}
	planned, err := GC(context.Background(), options)
	if err != nil || len(planned.Entries) != 1 || !planned.Entries[0].Eligible {
		t.Fatalf("GC plan = %#v, %v", planned.Entries, err)
	}
	// A stale Applied field on an ineligible row must not turn a repository
	// which this iteration never attempted into a retired repository.
	planned.Entries = append(planned.Entries, GCEntry{
		Task: task, Repository: "acme/held", WorktreeDir: filepath.Join(fixture.projectsRoot, "held"), Applied: true,
	}, GCEntry{
		Task: task, Repository: "acme/missing", WorktreeDir: filepath.Join(fixture.projectsRoot, "missing"),
		Eligible: true, Applied: true,
	})
	if err := applyGC(context.Background(), options, &planned); err != nil {
		t.Fatal(err)
	}
	if !planned.Entries[0].Applied || planned.Entries[2].Error == "" || !planned.Entries[2].Applied || len(planned.PartialTasks) != 1 ||
		len(planned.PartialTasks[0].Retired) != 1 || planned.PartialTasks[0].Retired[0] != "acme/app" ||
		len(planned.PartialTasks[0].LeftAlone) != 2 || planned.PartialTasks[0].LeftAlone[0] != "acme/held" ||
		planned.PartialTasks[0].LeftAlone[1] != "acme/missing" {
		t.Fatalf("GC accounting = %#v, partial=%#v", planned.Entries, planned.PartialTasks)
	}
	if _, err := os.Lstat(created.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("retired checkout remains: %v", err)
	}
}

//nolint:paralleltest // the Git fixture configures process-wide environment
func TestE2EGCApplyLeavesRefusedAndMissingCandidates(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "held-by-grace-window")
	installMergedPullRequestFixture(t, head, mergedAt)
	outcome := GCOutcome{Entries: []GCEntry{{
		Task: "held-by-grace-window", Repository: "acme/app", WorktreeDir: created.WorktreeDir,
		Eligible: true, Branch: created.Branch, HeadSHA: head,
	}}}
	options := GCOptions{ProjectsRoot: fixture.projectsRoot, OlderThan: 24 * time.Hour,
		Now: func() time.Time { return mergedAt.Add(time.Hour) }}
	if err := applyGC(context.Background(), options, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Entries[0].Applied || !strings.Contains(outcome.Entries[0].Error, "cleanup did not retire") {
		t.Fatalf("unfinished candidate was retired: %#v", outcome.Entries[0])
	}
	missing := GCOutcome{Entries: []GCEntry{{
		Task: "missing", Repository: "acme/app", WorktreeDir: filepath.Join(fixture.projectsRoot, "missing"),
		Eligible: true, Branch: "wb/missing", HeadSHA: strings.Repeat("a", 40),
	}}}
	if err := applyGC(context.Background(), options, &missing); err != nil {
		t.Fatal(err)
	}
	if missing.Entries[0].Applied || missing.Entries[0].Error == "" || strings.Contains(missing.Entries[0].Error, "cleanup did not retire") {
		t.Fatalf("missing candidate was retired: %#v", missing.Entries[0])
	}
}

//nolint:paralleltest // the Git fixture configures process-wide environment
func TestE2EGCRenamedBranchWarningRemainsAdvisory(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "renamed-warning", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "checkout", "-b", "renamed-warning-current")
	result := ListResult{Task: "renamed-warning", Repository: "acme/app", WorktreeDir: worktree,
		Branch: "renamed-warning-current", Clean: true, LastActivityAt: time.Now().UTC()}
	warning := renamedBranchWarning(result)
	if !strings.Contains(warning, created[0].Branch) || !strings.Contains(warning, result.Branch) {
		t.Fatalf("renamed branch warning = %q", warning)
	}
	entry := classifyForGC(result, GCOptions{}, time.Now().UTC())
	if entry.Eligible || len(entry.Warnings) == 0 || entry.Warnings[0] != warning {
		t.Fatalf("branch rename changed GC eligibility or lost its warning: %#v", entry)
	}
}
