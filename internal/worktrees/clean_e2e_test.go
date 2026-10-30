//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// addOwnerRepositoryToFixture gives a fixture a canonical clone under another
// owner, with its own bare origin: the multi-org shape `wb clean --filter`
// must select from.
func addOwnerRepositoryToFixture(t *testing.T, fixture *gitFixture, owner, repository string) string {
	t.Helper()
	root := filepath.Dir(fixture.projectsRoot)
	remote := filepath.Join(root, owner+"-"+repository+"-remote.git")
	gitTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	canonical := filepath.Join(fixture.projectsRoot, owner, repository)
	gitTest(t, root, "clone", remote, canonical)
	configureGitUser(t, canonical)
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# "+repository+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "add", "README.md")
	gitTest(t, canonical, "commit", "-m", "initial")
	gitTest(t, canonical, "push", "-u", "origin", "main")
	return canonical
}

// TestCleanFilterSelectsOneOwnerOfAMultiOrgFleet is the 0.202.0 defect: a
// glob filter silently matched nothing. Every spelling must select exactly the
// named owner's repositories, and a filter matching nothing must say so.
//
//nolint:paralleltest // the git fixture pins HOME and WB_PROJECTS_ROOT with t.Setenv.
func TestE2ECleanFilterSelectsOneOwnerOfAMultiOrgFleet(t *testing.T) {
	fixture := newGitFixture(t)
	addOwnerRepositoryToFixture(t, fixture, "other", "svc")
	_, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "clean-filter")
	installMergedPullRequestFixture(t, head, mergedAt)
	now := func() time.Time { return mergedAt.Add(48 * time.Hour) }
	run := func(options CleanOptions) (CleanOutcome, error) {
		options.ProjectsRoot, options.Now, options.SessionFreshness = fixture.projectsRoot, now, DisableSessionFreshness
		options.SkipSizes = true
		return Clean(context.Background(), options)
	}
	for _, selection := range []CleanOptions{{Filter: "acme/*"}, {Filter: "ACME/"}, {Orgs: []string{"acme"}}, {Repository: "acme/app"}} {
		outcome, err := run(selection)
		if err != nil {
			t.Fatalf("%+v: %v", selection, err)
		}
		if !reflect.DeepEqual(outcome.Selection.Repositories, []string{"acme/app"}) {
			t.Fatalf("%+v selected %v", selection, outcome.Selection.Repositories)
		}
		if len(outcome.Worktrees.Entries) != 1 || outcome.Worktrees.Entries[0].Task != "clean-filter" || !outcome.Worktrees.Entries[0].Eligible {
			t.Fatalf("%+v worktrees %+v", selection, outcome.Worktrees.Entries)
		}
	}
	for _, narrowed := range []CleanOptions{{Filter: "acme/*", Branch: "no-such-branch"}, {Filter: "acme/*", Only: GCClassDirty}} {
		outcome, err := run(narrowed)
		if err != nil || len(outcome.Worktrees.Entries) != 0 {
			t.Fatalf("%+v must select no checkout: %+v %v", narrowed, outcome.Worktrees, err)
		}
	}
	other, err := run(CleanOptions{Filter: "other/*"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(other.Selection.Repositories, []string{"other/svc"}) || len(other.Worktrees.Entries) != 0 {
		t.Fatalf("other owner selection %v leaked %+v", other.Selection.Repositories, other.Worktrees.Entries)
	}
	_, err = run(CleanOptions{Filter: "nomatch/*"})
	if !errors.Is(err, ErrNoRepositoriesMatched) || !strings.Contains(err.Error(), "0 repositories matched --filter nomatch/*") {
		t.Fatalf("an unmatched filter must be a finding, got %v", err)
	}
}

// TestCleanApplyRetiresOnlyWhatTheDryRunSelected runs the same selection as a
// plan and as an apply: the apply retires the selected owner's finished
// checkout and nothing else.
//
//nolint:paralleltest // the git fixture pins HOME and WB_PROJECTS_ROOT with t.Setenv.
func TestE2ECleanApplyRetiresOnlyWhatTheDryRunSelected(t *testing.T) {
	fixture := newGitFixture(t)
	addOwnerRepositoryToFixture(t, fixture, "other", "svc")
	result, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "clean-apply")
	installMergedPullRequestFixture(t, head, mergedAt)
	options := CleanOptions{
		ProjectsRoot: fixture.projectsRoot, Orgs: []string{"acme"}, Only: CleanOnlyLanded, SkipSizes: true,
		SessionFreshness: DisableSessionFreshness, Now: func() time.Time { return mergedAt.Add(48 * time.Hour) },
	}
	plan, err := Clean(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	options.Apply = true
	applied, err := Clean(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Totals["eligible"] != 1 || applied.Totals["retired"] != 1 || len(applied.Worktrees.Entries) != len(plan.Worktrees.Entries) {
		t.Fatalf("plan %v apply %v", plan.Totals, applied.Totals)
	}
	if _, err := os.Stat(result.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("the selected checkout survived the apply: %v", err)
	}
}

//nolint:paralleltest // the git fixture pins HOME and WB_PROJECTS_ROOT with t.Setenv.
func TestE2EResolveRetireNameReadsTheRealInventory(t *testing.T) {
	fixture := newGitFixture(t)
	result, _, _ := prepareMergedTaskInFixture(t, fixture, "batch5-assembly")
	task, err := ResolveRetireName(context.Background(), fixture.projectsRoot, "", result.Branch)
	if err != nil || task != "batch5-assembly" {
		t.Fatal(task, err)
	}
	if err := (&TaskNotFoundError{Task: "batch5"}).Error(); err != `WB worktree task "batch5" was not found` {
		t.Fatal(err)
	}
}
