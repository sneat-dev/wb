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
			created, head, mergedAt := prepareMergedTaskInFixture(t, fixture, "scoped-healthy")
			installMergedPullRequestFixtures(t, []string{head}, mergedAt)
			effort := "unattributed-effort"
			if associated {
				effort = "scoped-healthy"
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
				if _, err := LoadWorkLogView(context.Background(), LoadWorkLogOptions{ProjectsRoot: fixture.projectsRoot, Worktree: created.WorktreeDir}); err != nil {
					t.Fatalf("selected native proof changed: %v", err)
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
