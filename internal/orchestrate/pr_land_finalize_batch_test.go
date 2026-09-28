package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

//nolint:paralleltest // newLandFixture installs a process-wide fake gh and Git environment.
func TestFinalizeLandedPullRequestVerifiesEvidenceBeforeRetirement(t *testing.T) {
	for _, tc := range []struct {
		name             string
		merged           bool
		baseRef          string
		projectsRootMode string
		failStep         string
		remoteHeadSHA    string
		wantCode         string
		wantCommand      string
		wantError        string
		wantOnBase       bool
	}{
		{name: "server has not confirmed merge", wantCode: LandRefusalLandingUnverified, wantCommand: "wb pr land acme/app#7"},
		{name: "merge commit absent from requested base", merged: true, baseRef: "missing-base", wantCode: LandRefusalLandingUnverified, wantCommand: "wb pr land acme/app#7"},
		{name: "canonical root missing", merged: true, projectsRootMode: "empty", wantCode: LandRefusalCanonicalSync, wantCommand: "wb sync --filter acme/app", wantOnBase: true},
		{name: "canonical clone missing", merged: true, projectsRootMode: "other", wantCode: LandRefusalCanonicalSync, wantCommand: "wb sync --filter acme/app", wantOnBase: true},
		{name: "branch deletion lacks exact head", merged: true, wantError: "without the merged pull request head SHA", wantOnBase: true},
		{name: "verification read fails", merged: true, failStep: "read", wantError: "Bad Gateway"},
		{name: "base comparison fails", merged: true, failStep: "compare", wantError: "deliberately failed"},
	} {
		//nolint:paralleltest // Child fixtures each change process-wide PATH and Git environment.
		t.Run(tc.name, func(t *testing.T) {
			fixture := newLandFixture(t, "bump/finalize-evidence", "go.mod")
			if tc.merged {
				fixture.writeState(t, "merged", "true")
				fixture.writeState(t, "pr-state", "closed")
			}
			options := landOptions(fixture)
			switch tc.projectsRootMode {
			case "empty":
				options.ProjectsRoot = ""
			case "other":
				options.ProjectsRoot = t.TempDir()
			}
			view, err := ReadPullRequest(context.Background(), options.Repository, "7")
			if err != nil {
				t.Fatal(err)
			}
			if tc.baseRef != "" {
				view.Base.Ref = tc.baseRef
			}
			switch tc.failStep {
			case "read":
				fixture.writeState(t, "pr-view-fail-count", "1")
			case "compare":
				fixture.writeState(t, "fail-compare-once", "1")
			}
			result := PullRequestLandResult{Repository: options.Repository, PullRequest: 7, Evidence: map[string]string{}}
			got, err := finalizeLandedPullRequest(context.Background(), options, result, view, tc.remoteHeadSHA, "7")
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q; receipt = %+v", err, tc.wantError, got)
				}
			} else if err != nil {
				t.Fatalf("finalize: %v; receipt = %+v", err, got)
			}
			if got.LandingOnBase != tc.wantOnBase {
				t.Fatalf("landing on base = %t, want %t; receipt = %+v", got.LandingOnBase, tc.wantOnBase, got)
			}
			if tc.wantCode != "" && (got.Outcome != LandFindings || got.RefusalCode != tc.wantCode || got.SanctionedCommand != tc.wantCommand) {
				t.Fatalf("finding receipt = %+v, want code %q and command %q", got, tc.wantCode, tc.wantCommand)
			}
			if tc.merged && tc.failStep != "read" && got.Evidence["merge_commit"] == "" {
				t.Fatalf("merged receipt omitted merge commit: %+v", got)
			}
			if !tc.merged && got.MergeSHA != "" {
				t.Fatalf("unverified merge has SHA: %+v", got)
			}
			if fixture.readState(t, "merged") != map[bool]string{true: "true", false: "false"}[tc.merged] {
				t.Fatalf("finalization changed remote merge state")
			}
		})
	}
}

//nolint:paralleltest // newLandFixture installs a process-wide fake gh and Git environment.
func TestFinalizeLandedPullRequestReportsIncompleteWorktreeRetirement(t *testing.T) {
	fixture := newLandFixture(t, "bump/finalize-cleanup", "go.mod")
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projects, Operation: "finalize-cleanup",
		Branch: "bump/finalize-cleanup", BranchChosen: true, Resume: true,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "unfinished.txt"), []byte("work in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.writeState(t, "merged", "true")
	fixture.writeState(t, "pr-state", "closed")
	options := landOptions(fixture)
	options.Keep = false
	view, err := ReadPullRequest(context.Background(), options.Repository, "7")
	if err != nil {
		t.Fatal(err)
	}
	result := PullRequestLandResult{Repository: options.Repository, PullRequest: 7, Evidence: map[string]string{}}
	got, err := finalizeLandedPullRequest(context.Background(), options, result, view, fixture.headSHA, "7")
	if err != nil {
		t.Fatalf("finalize: %v; receipt = %+v", err, got)
	}
	if got.Outcome != LandFindings || got.RefusalCode != "cleanup-incomplete" || got.SanctionedCommand != "wb worktree gc --apply" {
		t.Fatalf("cleanup finding = %+v", got)
	}
	if !got.LandingOnBase || got.CanonicalSync == "" || !got.BranchDeleted || len(got.CleanupReports) == 0 {
		t.Fatalf("receipt omitted completed landing effects and cleanup report: %+v", got)
	}
	if _, err := os.Stat(created[0].WorktreeDir); err != nil {
		t.Fatalf("incomplete cleanup removed the worktree: %v", err)
	}
}
