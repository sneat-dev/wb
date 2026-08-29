package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAbortOrphanedClaimRequiresExactNegativeEvidenceAndSealsIt(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "orphaned-claim",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := readOrphanTestClaim(t, created[0].WorkLogPath)

	gitTest(t, fixture.canonical, "worktree", "remove", created[0].WorktreeDir)
	gitTest(t, fixture.canonical, "update-ref", "-d", "refs/heads/"+created[0].Branch)

	plan, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "orphaned-claim",
		Disposition:  AbortOrphaned,
		ClaimID:      claim.ClaimID,
		Actor:        "founder",
		Reason:       "the checkout and every branch ref were already lost",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || !plan[0].Eligible || plan[0].Applied || !plan[0].WorktreeGone {
		t.Fatalf("orphan plan = %#v", plan)
	}
	terminalPath := filepath.Join(filepath.Dir(filepath.Dir(created[0].WorkLogPath)), "terminals", claim.ClaimID+".json")
	if _, statErr := os.Stat(terminalPath); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run wrote terminal evidence: %v", statErr)
	}

	applied, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "orphaned-claim",
		Disposition:  AbortOrphaned,
		ClaimID:      claim.ClaimID,
		Actor:        "founder",
		Reason:       "the checkout and every branch ref were already lost",
		Apply:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || !applied[0].Applied || applied[0].Disposition != AbortOrphaned {
		t.Fatalf("orphan apply = %#v", applied)
	}
	var terminal workLogTerminalRecord
	raw, err := os.ReadFile(terminalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Disposition != string(AbortOrphaned) || terminal.FinalCommit != "" || terminal.Orphaned == nil ||
		terminal.Orphaned.Actor != "founder" || !terminal.Orphaned.WorktreeAbsent ||
		!terminal.Orphaned.RegistrationAbsent || !terminal.Orphaned.LocalBranchAbsent ||
		!terminal.Orphaned.RemoteBranchAbsent || !terminal.Orphaned.TerminalAbsent {
		t.Fatalf("orphan terminal evidence = %#v", terminal)
	}
}

func TestAbortOrphanedClaimRechecksAbsenceUnderClaimLock(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "orphaned-claim-race",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := readOrphanTestClaim(t, created[0].WorkLogPath)
	head := gitTestOutput(t, created[0].WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "worktree", "remove", created[0].WorktreeDir)
	gitTest(t, fixture.canonical, "update-ref", "-d", "refs/heads/"+created[0].Branch)

	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "orphaned-claim-race",
		Disposition:  AbortOrphaned,
		ClaimID:      claim.ClaimID,
		Actor:        "founder",
		Reason:       "confirmed lost",
		Apply:        true,
		beforeOrphanSeal: func() {
			gitTest(t, fixture.canonical, "update-ref", "refs/heads/"+created[0].Branch, head)
		},
	})
	if err == nil {
		t.Fatalf("orphan sealing ignored a reappeared branch: %#v", results)
	}
	terminalPath := filepath.Join(filepath.Dir(filepath.Dir(created[0].WorkLogPath)), "terminals", claim.ClaimID+".json")
	if _, statErr := os.Stat(terminalPath); !os.IsNotExist(statErr) {
		t.Fatalf("refused race wrote terminal evidence: %v", statErr)
	}
}

func TestAbortOrphanedClaimRefusesEveryNonAbsentPredicate(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *gitFixture, CreateResult, workLogClaim)
		want  string
	}{
		{name: "worktree path", setup: func(*testing.T, *gitFixture, CreateResult, workLogClaim) {}, want: "worktree path still exists"},
		{name: "registration", setup: func(t *testing.T, _ *gitFixture, created CreateResult, _ workLogClaim) {
			if err := os.RemoveAll(created.WorktreeDir); err != nil {
				t.Fatal(err)
			}
		}, want: "remains registered"},
		{name: "local branch", setup: func(t *testing.T, fixture *gitFixture, created CreateResult, _ workLogClaim) {
			gitTest(t, fixture.canonical, "worktree", "remove", created.WorktreeDir)
		}, want: "local branch"},
		{name: "remote branch", setup: func(t *testing.T, fixture *gitFixture, created CreateResult, _ workLogClaim) {
			gitTest(t, created.WorktreeDir, "push", "-u", "origin", created.Branch)
			gitTest(t, fixture.canonical, "worktree", "remove", created.WorktreeDir)
			gitTest(t, fixture.canonical, "update-ref", "-d", "refs/heads/"+created.Branch)
		}, want: "remote branch"},
		{name: "terminal record", setup: func(t *testing.T, fixture *gitFixture, created CreateResult, claim workLogClaim) {
			gitTest(t, fixture.canonical, "worktree", "remove", created.WorktreeDir)
			gitTest(t, fixture.canonical, "update-ref", "-d", "refs/heads/"+created.Branch)
			runDir, _, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = runDir.Close() }()
			if _, err := writeWorkLogTerminal(fixture.home, runDir, claim, claim.BaseSHA, "discarded", "", "", nil); err != nil {
				t.Fatal(err)
			}
		}, want: "terminal record already exists"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot, Operation: "orphan-refusal-" + strings.ReplaceAll(test.name, " ", "-"),
				WorkLog: WorkLogOptions{Model: "unknown"},
			})
			if err != nil {
				t.Fatal(err)
			}
			claim := readOrphanTestClaim(t, created[0].WorkLogPath)
			test.setup(t, fixture, created[0], claim)
			results, err := Abort(context.Background(), AbortOptions{ProjectsRoot: fixture.projectsRoot,
				Task: claim.Task, Disposition: AbortOrphaned, ClaimID: claim.ClaimID,
				Actor: "founder", Reason: "negative evidence audit"})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Eligible || !strings.Contains(results[0].Reason, test.want) {
				t.Fatalf("orphan refusal = %#v, want reason containing %q", results, test.want)
			}
		})
	}
}

func readOrphanTestClaim(t *testing.T, path string) workLogClaim {
	t.Helper()
	var claim workLogClaim
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &claim); err != nil {
		t.Fatal(err)
	}
	return claim
}
