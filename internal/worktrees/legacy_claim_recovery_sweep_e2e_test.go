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

//nolint:paralleltest // the Git and pull-request fixtures configure process-wide environment
func TestE2ELegacyMissingClaimRecoveryRefusesChangedImmutableEvidence(t *testing.T) {
	const task = "legacy-sweep-evidence"
	fixture, created, _, _, _ := prepareAbsorbedCandidate(t, task)
	integrationHead := gitTestOutput(t, fixture.canonical, "rev-parse", "integration/"+task)
	squashSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")
	installAbsorbingPullRequestFixture(t, integrationHead, squashSHA, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
	projection, err := readWorkLogProjection(created.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "claims", projection.ClaimID+".json")
	if err := os.Remove(claimPath); err != nil {
		t.Fatal(err)
	}
	options := AbortOptions{ProjectsRoot: fixture.projectsRoot, Task: task, Disposition: AbortDiscarded,
		AbsorbedBy: "77", DeleteRemote: true}
	plan, err := Abort(context.Background(), options)
	if err != nil || len(plan) != 1 || !plan[0].WorkLogRecoveryPlanned {
		t.Fatalf("recoverable baseline = %#v, %v", plan, err)
	}
	entry := plan[0].ListResult
	if _, err := planLegacyMissingClaimRecovery(fixture.home, options, entry); err != nil {
		t.Fatalf("unchanged historical evidence refused: %v", err)
	}
	bad := entry
	bad.Branch = "unexpected-branch"
	if _, err := planLegacyMissingClaimRecovery(fixture.home, options, bad); err == nil || !strings.Contains(err.Error(), "do not match exactly") {
		t.Fatalf("changed branch accepted: %v", err)
	}
	missingRun := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID)
	if err := os.Rename(missingRun, missingRun+".missing"); err != nil {
		t.Fatal(err)
	}
	if _, err := planLegacyMissingClaimRecovery(fixture.home, options, entry); err == nil || !strings.Contains(err.Error(), "historical Work Log run") {
		t.Fatalf("missing historical run accepted: %v", err)
	}
	if err := os.Rename(missingRun+".missing", missingRun); err != nil {
		t.Fatal(err)
	}
	outboxPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "outbox",
		projection.RunID+"-"+projection.ClaimID+"-claimed.json")
	if err := os.Rename(outboxPath, outboxPath+".missing"); err != nil {
		t.Fatal(err)
	}
	if _, err := planLegacyMissingClaimRecovery(fixture.home, options, entry); err == nil || !strings.Contains(err.Error(), "immutable claimed outbox") {
		t.Fatalf("missing immutable outbox accepted: %v", err)
	}
	if _, err := os.Lstat(claimPath); !os.IsNotExist(err) {
		t.Fatalf("preflight recreated private claim: %v", err)
	}
}
