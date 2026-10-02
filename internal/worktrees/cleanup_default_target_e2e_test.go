//go:build e2e

package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"github.com/sneat-dev/wb/internal/worktreelanding"
)

// These journeys reproduce the 2026-10-02 fleet state with real Git: an
// integration branch that landed on main by a merge commit and was then deleted
// on origin, the tasks recorded against it, and a task stacked on another
// task's branch. Only GitHub's pull-request index is stubbed, and it answers
// "no pull request" for every question, exactly as it did for heads that were
// never themselves the head of a pull request.

// integrationScratch is a second clone of the fixture remote, used to merge
// branches the way a merger does without touching the canonical checkout.
func integrationScratch(t *testing.T, fixture *gitFixture) string {
	t.Helper()
	parent := t.TempDir()
	scratch := filepath.Join(parent, "scratch")
	gitTest(t, parent, "clone", fixture.remote, scratch)
	gitTest(t, scratch, "config", "user.name", "WB Test")
	gitTest(t, scratch, "config", "user.email", "wb@example.test")
	return scratch
}

func commitAndPushTaskWork(t *testing.T, result CreateResult, file string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, file), []byte(file+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, result.WorktreeDir, "add", file)
	gitTest(t, result.WorktreeDir, "commit", "-m", "work in "+file)
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)
	return gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
}

func createTaskOnBase(t *testing.T, fixture *gitFixture, task, base string) CreateResult {
	t.Helper()
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: task, Base: base, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created[0]
}

// integrationBranchTask builds a task recorded against the integration branch
// `cockpit-ux`. When landed, the task branch is merged into the integration
// branch and that branch into main by merge commits; either way origin then
// deletes the integration branch and the canonical clone prunes it.
func integrationBranchTask(t *testing.T, task string, landed bool) (*gitFixture, CreateResult, string) {
	t.Helper()
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "branch", "cockpit-ux", "main")
	gitTest(t, fixture.canonical, "push", "origin", "cockpit-ux")
	result := createTaskOnBase(t, fixture, task, "cockpit-ux")
	head := commitAndPushTaskWork(t, result, "feature.txt")
	scratch := integrationScratch(t, fixture)
	if landed {
		gitTest(t, scratch, "checkout", "cockpit-ux")
		gitTest(t, scratch, "merge", "--no-ff", "origin/"+result.Branch, "-m", "merge task into cockpit-ux")
		gitTest(t, scratch, "push", "origin", "cockpit-ux")
		gitTest(t, scratch, "checkout", "main")
		gitTest(t, scratch, "merge", "--no-ff", "cockpit-ux", "-m", "merge cockpit-ux into main")
		gitTest(t, scratch, "push", "origin", "main")
	}
	gitTest(t, scratch, "push", "origin", ":cockpit-ux")
	gitTest(t, fixture.canonical, "fetch", "--prune", "origin")
	installPullRequestResponses(t, "[]", "")
	return fixture, result, head
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB fixture state.
func TestE2ECleanupRetiresATaskWhoseIntegrationBranchLandedAndWasDeleted(t *testing.T) {
	const task = "cv-gofix1"
	fixture, result, head := integrationBranchTask(t, task, true)
	mainHead := remoteBranchForTest(t, fixture.canonical, "main")
	now := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	wantProof := "contained in origin/main at " + shortSHA(mainHead) + ", via recorded base cockpit-ux (absent)"

	// Every verb that discovers a task offline must still see this one. The
	// pruned origin/cockpit-ux ref used to turn it into a malformed candidate.
	listed, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: task})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Diagnostics) != 0 || len(listed.Results) != 1 || !listed.Results[0].LocallyMerged ||
		listed.Results[0].Base != "main" || listed.Results[0].RecordedBase != "cockpit-ux" ||
		listed.Results[0].RecordedBaseState != worktreelanding.RecordedBaseAbsent {
		t.Fatalf("offline list after the recorded base was pruned = %#v", listed)
	}

	collected, err := GC(context.Background(), GCOptions{
		ProjectsRoot: fixture.projectsRoot, Tasks: []string{task},
		SessionFreshness: DisableSessionFreshness, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry := entryFor(t, collected, task); !entry.Eligible || len(collected.Diagnostics) != 0 {
		t.Fatalf("gc of a task whose integration branch landed = %#v, diagnostics %v", entry, collected.Diagnostics)
	}

	for name, explicit := range map[string]bool{"recorded base": false, "explicit --base main": true} {
		planned, err := Cleanup(context.Background(), CleanupOptions{
			ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main", ExplicitBase: explicit,
			Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(planned.Diagnostics) != 0 || len(planned.Results) != 1 {
			t.Fatalf("%s: cleanup plan = %#v", name, planned)
		}
		plan := planned.Results[0]
		if !plan.Eligible || plan.Applied || !plan.IntegratedAtOrigin || plan.HeadSHA != head ||
			plan.Base != "main" || plan.RecordedBase != "cockpit-ux" || plan.RemoteTargetSHA != mainHead {
			t.Fatalf("%s: cleanup plan = %#v", name, plan)
		}
		if !explicit && (plan.IntegrationProof != wantProof || plan.RecordedBaseState != worktreelanding.RecordedBaseAbsent || plan.TargetRejection != "") {
			t.Fatalf("%s: proof = %q, state = %q, rejection = %q; want %q", name, plan.IntegrationProof, plan.RecordedBaseState, plan.TargetRejection, wantProof)
		}
	}

	// --absorbed-by names the merge commit that carried the integration branch
	// into the default branch. It used to be verified against the absent
	// recorded base, which nothing can satisfy.
	assertAbsorbedByDefaultBranchLanding(t, fixture, task, mainHead, mainHead, now)
	// The same landing named by its pull request number: GitHub reports the
	// integration branch's pull request into main, whose head is not this task's.
	integrationTip := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main^2")
	gitTest(t, fixture.remote, "update-ref", "refs/pull/77/head", integrationTip)
	installAbsorbingPullRequestFixture(t, integrationTip, mainHead, now.Add(-time.Hour))
	assertAbsorbedByDefaultBranchLanding(t, fixture, task, "77", mainHead, now)

	// An explicit base origin does not have is the operator's answer, not a
	// reason to judge some other branch.
	absent, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Base: "cockpit-ux", ExplicitBase: true,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(absent.Results) != 0 || !strings.Contains(fmt.Sprint(absent.Diagnostics), "fetch exact origin/cockpit-ux target") {
		t.Fatalf("explicit absent base = %#v", absent)
	}

	// See TestCleanupMergedTaskWithRealGitData: the sandboxed Git capability
	// only authorizes the remote-branch deletion under the canonical clone.
	relocatedRemote := filepath.Join(fixture.canonical, ".wb-test-remote.git")
	if err := os.Rename(fixture.remote, relocatedRemote); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", relocatedRemote)
	projection, err := readWorkLogProjection(result.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main", Apply: true, DeleteRemote: true,
		ReportDir: filepath.Join(t.TempDir(), "audit"), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 1 || !applied.Results[0].Applied || !applied.Results[0].BranchDeleted ||
		!applied.Results[0].RemoteDeleted || applied.Results[0].IntegrationProof != wantProof {
		t.Fatalf("applied cleanup = %#v", applied)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("worktree still exists after cleanup: %v", statErr)
	}
	if remote := remoteBranchForTest(t, fixture.canonical, result.Branch); remote != "" {
		t.Fatalf("remote task branch still exists after cleanup: %s", remote)
	}
	var terminal workLogTerminalRecord
	readSealedJSON(t, filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals", projection.ClaimID+".json"), &terminal)
	assertSealedLandedOnDefaultBranch(t, terminal, head)
}

// assertSealedLandedOnDefaultBranch: work proved by containment in the default
// branch is sealed landed there, not on the recorded base that could not
// answer. landed_sha is the head, as the Work Log vocabulary defines for the
// contained proof; the head of main that contained it is in the cleanup report.
func assertSealedLandedOnDefaultBranch(t *testing.T, terminal workLogTerminalRecord, head string) {
	t.Helper()
	want := &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: head, Proof: worktreeclaims.LandedProofContained}
	if terminal.Disposition != "landed" || terminal.FinalCommit != head || !worktreeclaims.SameLandedEvidence(terminal.Landed, want) {
		t.Fatalf("terminal = %q at %s with %#v, want landed with %#v", terminal.Disposition, terminal.FinalCommit, terminal.Landed, want)
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB fixture state.
func TestE2EAbsentRecordedBaseRefusesAnUnlandedHeadWithoutHidingTheTask(t *testing.T) {
	const task = "cv-unlanded"
	fixture, result, head := integrationBranchTask(t, task, false)
	mainHead := remoteBranchForTest(t, fixture.canonical, "main")
	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main",
		Now: func() time.Time { return time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Diagnostics) != 0 || len(planned.Results) != 1 {
		t.Fatalf("an absent recorded base hid the task: %#v", planned)
	}
	plan := planned.Results[0]
	if plan.Eligible || plan.IntegratedAtOrigin || plan.IntegrationProof != "" || plan.RecordedBaseState != worktreelanding.RecordedBaseAbsent {
		t.Fatalf("unlanded head with an absent recorded base = %#v", plan)
	}
	for _, want := range []string{
		"current branch head " + shortSHA(head) + " is not integrated into the exact origin target origin/main at " + shortSHA(mainHead),
		"(pushed to origin/" + result.Branch + ", awaiting merge)",
		"recorded base cockpit-ux is absent",
		"GitHub has no exact merged receipt into default branch main for head " + head,
	} {
		if !strings.Contains(plan.Reason, want) {
			t.Fatalf("refusal = %q, want it to contain %q", plan.Reason, want)
		}
	}
	if _, statErr := os.Stat(result.WorktreeDir); statErr != nil {
		t.Fatalf("unlanded worktree was touched: %v", statErr)
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB fixture state.
func TestE2ECleanupRetiresATaskStackedOnATaskBranchThatLanded(t *testing.T) {
	fixture := newGitFixture(t)
	base := createTaskOnBase(t, fixture, "cv-t1-schema-v2", "main")
	baseHead := commitAndPushTaskWork(t, base, "schema.txt")
	leaf := createTaskOnBase(t, fixture, "cv-t2-metrics", base.Branch)
	leafHead := commitAndPushTaskWork(t, leaf, "metrics.txt")
	// The merger batched both branches straight onto the integration line, so
	// the leaf never became part of its own recorded base.
	scratch := integrationScratch(t, fixture)
	gitTest(t, scratch, "merge", "--no-ff", "origin/"+base.Branch, "-m", "merge schema")
	gitTest(t, scratch, "merge", "--no-ff", "origin/"+leaf.Branch, "-m", "merge metrics")
	gitTest(t, scratch, "push", "origin", "main")
	gitTest(t, fixture.canonical, "fetch", "origin")
	installPullRequestResponses(t, "[]", "")
	mainHead := remoteBranchForTest(t, fixture.canonical, "main")
	now := func() time.Time { return time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC) }

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cv-t2-metrics", Base: "main", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Diagnostics) != 0 || len(planned.Results) != 1 {
		t.Fatalf("stacked cleanup plan = %#v", planned)
	}
	plan := planned.Results[0]
	wantProof := "contained in origin/main at " + shortSHA(mainHead) + ", via recorded base " + base.Branch + " (integrated into origin/main)"
	if !plan.Eligible || !plan.IntegratedAtOrigin || plan.HeadSHA != leafHead || plan.Base != "main" ||
		plan.RecordedBase != base.Branch || plan.RecordedBaseState != worktreelanding.RecordedBaseIntegrated ||
		plan.RemoteTargetSHA != mainHead || plan.IntegrationProof != wantProof {
		t.Fatalf("stacked cleanup plan = %#v, want proof %q", plan, wantProof)
	}

	assertAbsorbedByDefaultBranchLanding(t, fixture, "cv-t2-metrics", mainHead, mainHead, now())

	// Naming the recorded base explicitly asks the original question, and gets
	// the original answer with the target it was compared against.
	explicit, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cv-t2-metrics", Base: base.Branch, ExplicitBase: true, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(explicit.Results) != 1 || explicit.Results[0].Eligible || explicit.Results[0].Base != base.Branch ||
		explicit.Results[0].RecordedBase != "" || explicit.Results[0].IntegrationProof != "" {
		t.Fatalf("explicit recorded base = %#v", explicit)
	}
	wantRefusal := "current branch head " + shortSHA(leafHead) + " is not integrated into the exact origin target origin/" +
		base.Branch + " at " + shortSHA(baseHead) + " (pushed to origin/" + leaf.Branch + ", awaiting merge)"
	if explicit.Results[0].Reason != wantRefusal {
		t.Fatalf("explicit recorded base refusal = %q, want %q", explicit.Results[0].Reason, wantRefusal)
	}

	relocatedRemote := filepath.Join(fixture.canonical, ".wb-test-remote.git")
	if err := os.Rename(fixture.remote, relocatedRemote); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", relocatedRemote)
	terminal, _ := sealedByCleanup(t, fixture, "cv-t2-metrics", leaf.WorktreeDir, now())
	assertSealedLandedOnDefaultBranch(t, terminal, leafHead)
}

// assertAbsorbedByDefaultBranchLanding proves cleanup and abort both accept a
// landing commit on the default branch for a task whose recorded base is not
// the default branch.
func assertAbsorbedByDefaultBranchLanding(t *testing.T, fixture *gitFixture, task, pointer, landing string, now time.Time) {
	t.Helper()
	absorbed, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main", AbsorbedBy: pointer,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(absorbed.Results) != 1 || !absorbed.Results[0].Eligible || !absorbed.Results[0].AbsorbedAtOrigin ||
		absorbed.Results[0].AbsorbedBySHA != landing || absorbed.Results[0].AbsorbedByRejection != "" {
		t.Fatalf("cleanup --absorbed-by %s = %#v", pointer, absorbed)
	}
	aborted, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main",
		Disposition: AbortDiscarded, AbsorbedBy: pointer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(aborted) != 1 || !aborted[0].Eligible || aborted[0].Applied {
		t.Fatalf("abort --absorbed-by %s = %#v", pointer, aborted)
	}
}

// installCommitIndexOutage serves the closed-pull-request listing and fails
// GitHub's commit-to-pull-request index, the second question an absent
// recorded base now asks.
func installCommitIndexOutage(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := `#!/bin/sh
case "$3" in
*/commits/*) echo "gh: commit index unavailable (HTTP 502)" >&2; exit 1 ;;
esac
printf '[]\n'
`
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB fixture state.
func TestE2EAbsentRecordedBaseKeepsAGitHubOutageADiagnostic(t *testing.T) {
	const task = "cv-outage"
	fixture, _, _ := integrationBranchTask(t, task, true)
	installCommitIndexOutage(t)
	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main",
		Now: func() time.Time { return time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// Evidence that could not be read is not evidence of absence: nothing is
	// planned, and the failure is reported for this task alone.
	if len(planned.Results) != 0 || !strings.Contains(fmt.Sprint(planned.Diagnostics), "commit index unavailable") {
		t.Fatalf("cleanup during a commit-index outage = %#v", planned)
	}
}
