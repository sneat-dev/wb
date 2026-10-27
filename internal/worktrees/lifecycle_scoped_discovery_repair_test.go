package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Native fixtures own process-wide Git/WB environment.
//
//nolint:paralleltest
func TestCleanupScopedDiscoveryPreservesUnattributedEvidenceAndSelectedCustody(t *testing.T) {
	for _, associated := range []bool{false, true} {
		name := "unrelated uncertainty permits guarded cleanup"
		if associated {
			name = "selected associated effort blocks cleanup"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newGitFixture(t)
			configureFixtureSharedWorktrees(t, fixture)
			createdRows, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "scoped-healthy", WorkLog: WorkLogOptions{EffortID: "shared-effort", Model: "unknown"}})
			if err != nil {
				t.Fatal(err)
			}
			created := createdRows[0]
			if err := os.WriteFile(filepath.Join(created.WorktreeDir, "feature.txt"), []byte("scoped native helper\n"), 0600); err != nil {
				t.Fatal(err)
			}
			gitTest(t, created.WorktreeDir, "add", "feature.txt")
			gitTest(t, created.WorktreeDir, "commit", "-m", "test: native helper")
			head := gitTestOutput(t, created.WorktreeDir, "rev-parse", "HEAD")
			gitTest(t, created.WorktreeDir, "push", "-u", "origin", created.Branch)
			gitTest(t, fixture.canonical, "merge", "--no-ff", created.Branch, "-m", "merge helper")
			gitTest(t, fixture.canonical, "push", "origin", "main")
			mergedAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
			installMergedPullRequestFixtures(t, []string{head}, mergedAt)
			view, err := LoadWorkLogView(context.Background(), LoadWorkLogOptions{ProjectsRoot: fixture.projectsRoot, Worktree: created.WorktreeDir})
			if err != nil || view.Claim == nil || view.Claim.Task != "scoped-healthy" || view.Claim.EffortID != "shared-effort" {
				t.Fatalf("native shared-effort identity: %+v %v", view, err)
			}
			claimBefore, err := os.ReadFile(view.Claim.ClaimPath)
			if err != nil {
				t.Fatal(err)
			}
			effort := "unattributed-effort"
			if associated {
				effort = "shared-effort"
			}
			obstruction := filepath.Join(fixture.home, "worklogs", effort, "runs", "obstructed-run", "claims")
			if err := os.MkdirAll(filepath.Dir(obstruction), 0700); err != nil {
				t.Fatal(err)
			}
			evidence := []byte("private obstructed claim inventory")
			if err := os.WriteFile(obstruction, evidence, 0600); err != nil {
				t.Fatal(err)
			}
			outcome, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "scoped-healthy", Apply: true, OlderThan: 0, Now: func() time.Time { return mergedAt.Add(time.Hour) }})
			if err != nil {
				t.Fatalf("scoped cleanup: %v", err)
			}
			if len(outcome.Results) != 1 {
				t.Fatalf("selected result lost: %+v", outcome)
			}
			result := outcome.Results[0]
			var found *ListDiagnostic
			for i := range outcome.Diagnostics {
				if outcome.Diagnostics[i].Path == obstruction {
					found = &outcome.Diagnostics[i]
				}
			}
			if found == nil {
				t.Fatalf("discovery uncertainty hidden: %+v", outcome.Diagnostics)
			}
			if associated {
				if found.Task != "scoped-healthy" || found.DiscoveryScope != "selected-associated-effort" || result.Applied || result.Eligible || !strings.Contains(result.Reason, "malformed candidate") {
					t.Fatalf("selected custody not fenced: result=%+v diagnostic=%+v", result, found)
				}
				if _, err := os.Stat(created.WorktreeDir); err != nil {
					t.Fatalf("blocked checkout removed: %v", err)
				}
				claimAfter, err := os.ReadFile(view.Claim.ClaimPath)
				if err != nil || string(claimAfter) != string(claimBefore) {
					t.Fatalf("blocked selected immutable claim changed: %v", err)
				}
				current, err := LoadWorkLogView(context.Background(), LoadWorkLogOptions{ProjectsRoot: fixture.projectsRoot, Worktree: created.WorktreeDir})
				if err != nil || current.Claim == nil || current.Claim.Lifecycle != "active" {
					t.Fatalf("selected native proof finalized: %+v %v", current, err)
				}
			} else {
				if found.Task != "" || found.DiscoveryScope != "unattributed" || !result.Applied {
					t.Fatalf("unrelated uncertainty acquired cleanup authority: result=%+v diagnostic=%+v", result, found)
				}
				if _, err := os.Stat(created.WorktreeDir); !os.IsNotExist(err) {
					t.Fatalf("guarded cleanup not applied: %v", err)
				}
			}
			got, err := os.ReadFile(obstruction)
			if err != nil || string(got) != string(evidence) {
				t.Fatalf("unknown evidence mutated: %q %v", got, err)
			}
		})
	}
}

//nolint:paralleltest // native fixture scopes process-wide WB configuration.
func TestCleanupMissingSelectionWithUnknownDiscoveryDoesNotClaimAbsence(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	obstruction := filepath.Join(fixture.home, "worklogs", "unknown-effort", "runs")
	if err := os.MkdirAll(filepath.Dir(obstruction), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obstruction, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "absent-task", Apply: true})
	if err == nil || !strings.Contains(err.Error(), "incomplete scoped discovery") || strings.Contains(err.Error(), "was not found") {
		t.Fatalf("false complete absence: %v", err)
	}
	got, readErr := os.ReadFile(obstruction)
	if readErr != nil || string(got) != "preserve" {
		t.Fatalf("unknown namespace changed: %q %v", got, readErr)
	}
}
