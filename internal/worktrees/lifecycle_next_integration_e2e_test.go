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

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktreelanding"
)

//nolint:paralleltest // Native Git and the existing hosted-receipt fixture configure process-wide environment.
func TestE2ELifecycleNextDeletedTargetReceiptRetainsNativeCommitRefusal(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	absent := strings.Repeat("f", 40)
	if _, err := git(context.Background(), fixture.canonical, "cat-file", "-e", absent+"^{commit}"); err == nil {
		t.Fatal("fixture unexpectedly contains absent receipt commit")
	}
	installMergedPullRequestFixtureWithMerge(t, head, absent, time.Now().UTC())
	inspection := lifecycleInspection{ctx: context.Background(), home: fixture.home, canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app", base: "feature/test", head: head, withGitHub: true}
	err := inspection.checkGitHubIntegration()
	if err == nil || !strings.Contains(err.Error(), "verify merged receipt #17 against fetched origin/main") {
		t.Fatalf("deleted target receipt refusal=%+v %v", inspection.result, err)
	}
	if inspection.result.RemoteTargetSHA != head || inspection.result.AbsorbedAtOrigin || inspection.result.MergedPullRequest != nil || inspection.base != "feature/test" {
		t.Fatalf("failed native receipt granted widened authority: %+v", inspection)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
		t.Fatalf("receipt refusal changed HEAD: %s", got)
	}
}

//nolint:paralleltest // Native Git and the existing hosted-receipt fixture configure process-wide environment.
func TestE2ELifecycleNextExplicitAbsorptionKeepsNativeObjectRefusal(t *testing.T) {
	fixture := newGitFixture(t)
	installPullRequestResponses(t, "[]", "")
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	remoteMain := strings.Fields(gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main"))
	if len(remoteMain) != 2 || remoteMain[1] != "refs/heads/main" {
		t.Fatalf("native remote main control=%q", remoteMain)
	}
	objects := filepath.Join(fixture.canonical, ".git", "objects")
	retained := objects + ".retained"
	mutated := false
	var nativeCause error
	var nativeOutput string
	observer := &lifecycleNextNativeGitObservation{Runner: runner.New(), after: func(_ string, args []string, result runner.Result, err error) {
		if !mutated && err == nil && reflect.DeepEqual(args, []string{"-C", fixture.canonical, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}"}) {
			if err := os.Rename(objects, retained); err != nil {
				t.Fatal(err)
			}
			mutated = true
		} else if mutated && err != nil && nativeCause == nil && len(args) > 2 && args[2] == "merge-tree" {
			nativeCause = err
			nativeOutput = strings.TrimSpace(result.Stderr)
		}
	}}
	t.Cleanup(func() {
		if mutated {
			if err := os.Rename(retained, objects); err != nil {
				t.Error(err)
			}
		}
	})
	inspection := lifecycleInspection{ctx: withGitRunner(context.Background(), observer), home: fixture.home, canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app", base: "main", head: head, withGitHub: true, absorbedBy: "HEAD"}
	err := inspection.checkGitHubIntegration()
	if !mutated || nativeCause == nil || nativeOutput == "" || err == nil || !errors.Is(err, nativeCause) || !strings.Contains(err.Error(), nativeOutput) || !strings.Contains(err.Error(), "merge "+head+" into "+head+" in "+fixture.canonical) {
		t.Fatalf("native absorption refusal=%+v %v", inspection.result, err)
	}
	if !inspection.result.IntegratedAtOrigin || inspection.result.RemoteTargetSHA != remoteMain[0] || inspection.result.AbsorbedAtOrigin || inspection.result.AbsorbedBySHA != "" {
		t.Fatalf("explicit failed proof changed ordinary containment: %+v", inspection.result)
	}
	if err := os.Rename(retained, objects); err != nil {
		t.Fatal(err)
	}
	mutated = false
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
		t.Fatalf("failed explicit proof changed HEAD: %s", got)
	}
}

func TestE2ELifecycleNextStateAppliesExactSourceReceiptTargetOverride(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	canonical := newLegacyClone(t, projects, "acme", "app")
	gitTest(t, canonical, "checkout", "-b", "feature")
	head := gitTestOutput(t, canonical, "rev-parse", "HEAD")
	proof := MergeReceiptCleanupProof{Repository: "acme/app", SourceTask: "task", SourceWorktree: canonical, SourceBranch: "feature", SourceSHA: head, CandidateSHA: head, LandingSHA: head, Target: "main"}
	inspection := lifecycleInspection{ctx: context.Background(), canonical: canonical, worktree: canonical, slug: "acme/app", task: "task", base: "recorded-target", withGitHub: true, policy: inspectPolicy{mergeReceiptProofs: []MergeReceiptCleanupProof{proof}}}
	if err := inspection.inspectState(); err != nil {
		t.Fatal(err)
	}
	if inspection.base != "main" || inspection.result.Base != "main" || inspection.result.RecordedBase != "recorded-target" || inspection.result.HeadSHA != head {
		t.Fatalf("source-bound override=%+v", inspection)
	}
	if got := gitTestOutput(t, canonical, "rev-parse", "HEAD"); got != head {
		t.Fatalf("inspection changed source HEAD: %s", got)
	}
}

//nolint:paralleltest // The native Git fixture pins process-wide HOME/WB/XDG; the observer mutates only its actual origin URL.
func TestE2ELifecycleNextDefaultBranchLookupPreservesNativeRefusal(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	missing := filepath.Join(t.TempDir(), "missing-origin.git")
	mutated := false
	nativeOutput := ""
	observer := &lifecycleNextNativeGitObservation{Runner: runner.New(), after: func(_ string, args []string, result runner.Result, err error) {
		if !mutated && len(args) > 2 && args[2] == "fetch" && err != nil && strings.Contains(result.CombinedOutput, "couldn't find remote ref") {
			gitTest(t, fixture.canonical, "remote", "set-url", "origin", missing)
			mutated = true
		} else if mutated && err != nil && reflect.DeepEqual(args, []string{"-C", fixture.canonical, "ls-remote", "--symref", "origin", "HEAD"}) {
			nativeOutput = strings.TrimSpace(result.CombinedOutput)
		}
	}}
	inspection := lifecycleInspection{ctx: withGitRunner(context.Background(), observer), home: fixture.home, canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app", base: "feature/deleted-target", head: head, withGitHub: true}
	err := inspection.checkGitHubIntegration()
	if !mutated || nativeOutput == "" || err == nil || !strings.Contains(err.Error(), "read origin default branch") || !strings.Contains(err.Error(), nativeOutput) || inspection.result.IntegratedAtOrigin || inspection.result.AbsorbedAtOrigin {
		t.Fatalf("native default-branch refusal=%+v %v observed=%v cause=%q", inspection.result, err, mutated, nativeOutput)
	}
	if got := gitTestOutput(t, fixture.canonical, "remote", "get-url", "origin"); got != missing {
		t.Fatalf("fixture origin drift not retained: %s", got)
	}
	if got := gitTestOutput(t, fixture.remote, "rev-parse", "HEAD"); got != head {
		t.Fatalf("refusal changed actual old origin: %s", got)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
		t.Fatalf("refusal changed actual local head: %s", got)
	}
}

// The four journeys below share one question: the recorded target is gone
// from origin, so the default branch is judged instead. What decides the
// answer is whether the candidate's own head is a Git ancestor of the fetched
// default branch. A GitHub receipt that is absent, or that names a merge
// commit the default branch does not contain, grants nothing either way.
//
//   origin/main:  I                      (the fixture's only pushed commit)
//   refused:      I --- W [--- M]        head W is local work, never on main
//   admitted:     I                      head is I itself, the tip of main

// deletedTargetLocalWork gives the canonical clone a commit of its own that
// origin/main does not have, and returns it with the fetched default head.
func deletedTargetLocalWork(t *testing.T, fixture *gitFixture) (work, defaultHead string) {
	t.Helper()
	defaultHead = gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	target := strings.Fields(gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main"))
	if len(target) != 2 || target[0] != defaultHead {
		t.Fatalf("native remote control=%v", target)
	}
	gitTest(t, fixture.canonical, "commit", "--allow-empty", "-m", "local work that never reached main")
	work = gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	if contained, err := isAncestor(context.Background(), fixture.canonical, work, defaultHead); err != nil || contained {
		t.Fatalf("fixture head must not be in the default branch: contained=%t err=%v", contained, err)
	}
	return work, defaultHead
}

// requireDeletedTargetRefusal asserts the candidate stays visible and refused:
// no error hides it, nothing calls it integrated, and cleanup's own safety
// decision says no for the stated reason.
func requireDeletedTargetRefusal(t *testing.T, inspection *lifecycleInspection, err error, defaultHead, rejection string) {
	t.Helper()
	result := inspection.result
	if err != nil || result.IntegratedAtOrigin || result.AbsorbedAtOrigin || result.RebaseMergedAtOrigin || result.LocallyMerged ||
		result.IntegrationProof != "" || result.RemoteTargetSHA != defaultHead || result.Base != "main" ||
		result.RecordedBaseState != worktreelanding.RecordedBaseAbsent || !strings.Contains(result.TargetRejection, rejection) {
		t.Fatalf("deleted-target candidate with a head outside the default branch was not refused: %+v %v", result, err)
	}
	result.Clean, result.HeadSHA = true, inspection.head
	if eligible, reason := cleanupSafetyEligibility(result, 0, time.Now(), false); eligible || !strings.Contains(reason, rejection) ||
		!strings.Contains(reason, "is not integrated into the exact origin target origin/main at "+shortSHA(defaultHead)) {
		t.Fatalf("cleanup eligibility = %t, reason = %q; want a refusal naming %q", eligible, reason, rejection)
	}
}

// requireDeletedTargetContainment asserts the admitted counterpart: proved by
// plain containment in the default branch and by nothing else.
func requireDeletedTargetContainment(t *testing.T, inspection *lifecycleInspection, err error, defaultHead, recordedBase string) {
	t.Helper()
	result := inspection.result
	want := "contained in origin/main at " + shortSHA(defaultHead) + ", via recorded base " + recordedBase + " (absent)"
	if err != nil || !result.IntegratedAtOrigin || result.AbsorbedAtOrigin || result.RebaseMergedAtOrigin ||
		result.IntegrationProof != want || result.TargetRejection != "" || result.RemoteTargetSHA != defaultHead ||
		result.Base != "main" || result.RecordedBase != recordedBase {
		t.Fatalf("head at the default branch tip = %+v %v, want proof %q", result, err, want)
	}
}

//nolint:paralleltest // The native Git and read-only hosted-observation fixtures pin process-wide environment.
func TestE2ELifecycleNextDeletedTargetRequiresExactMergedReceipt(t *testing.T) {
	fixture := newGitFixture(t)
	installPullRequestResponses(t, "[]", "")
	work, defaultHead := deletedTargetLocalWork(t, fixture)
	if _, err := fetchRemoteTargetHead(context.Background(), fixture.canonical, "feature/deleted-target"); !isMissingRemoteTargetError(err) {
		t.Fatalf("native deleted-target prerequisite=%v", err)
	}
	inspection := lifecycleInspection{ctx: context.Background(), home: fixture.home, canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app", base: "feature/deleted-target", head: work, withGitHub: true}
	err := inspection.checkGitHubIntegration()
	requireDeletedTargetRefusal(t, &inspection, err, defaultHead, "GitHub has no exact merged receipt into default branch main")
	if inspection.result.MergedPullRequest != nil {
		t.Fatalf("absent exact receipt produced a pull request: %+v", inspection.result.MergedPullRequest)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != work {
		t.Fatalf("refusal changed head: %s", got)
	}
}

//nolint:paralleltest // The native Git and read-only hosted-observation fixtures pin process-wide environment.
func TestE2ELifecycleNextDeletedTargetWithHeadAtDefaultBranchTipIsProvedByContainmentWithoutAReceipt(t *testing.T) {
	fixture := newGitFixture(t)
	installPullRequestResponses(t, "[]", "")
	defaultHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	inspection := lifecycleInspection{ctx: context.Background(), home: fixture.home, canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app", base: "feature/deleted-target", head: defaultHead, withGitHub: true}
	err := inspection.checkGitHubIntegration()
	requireDeletedTargetContainment(t, &inspection, err, defaultHead, "feature/deleted-target")
	if inspection.result.MergedPullRequest != nil {
		t.Fatalf("containment invented a pull request: %+v", inspection.result.MergedPullRequest)
	}
}

//nolint:paralleltest // The native Git and read-only hosted receipt fixtures pin process-wide environment.
func TestE2ELifecycleNextDeletedTargetRejectsUncontainedMergeCommit(t *testing.T) {
	fixture := newGitFixture(t)
	work, defaultHead := deletedTargetLocalWork(t, fixture)
	gitTest(t, fixture.canonical, "commit", "--allow-empty", "-m", "unattached local receipt commit")
	merge := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	if contained, err := isAncestor(context.Background(), fixture.canonical, merge, defaultHead); err != nil || contained || merge == work {
		t.Fatalf("native uncontained commit prerequisite=%v %v", contained, err)
	}
	// GitHub reports the exact head merged into main at a commit origin/main
	// does not contain. That is metadata, and it proves nothing.
	installMergedPullRequestFixtureWithMerge(t, work, merge, time.Now().UTC())
	inspection := lifecycleInspection{ctx: context.Background(), home: fixture.home, canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app", base: "feature/test", head: work, withGitHub: true}
	err := inspection.checkGitHubIntegration()
	requireDeletedTargetRefusal(t, &inspection, err, defaultHead, "merged receipt #17 commit "+merge+" is not contained in freshly fetched origin/main")
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != merge {
		t.Fatalf("refusal changed retained unattached commit: %s", got)
	}
	if got := gitTestOutput(t, fixture.remote, "rev-parse", "HEAD"); got != defaultHead {
		t.Fatalf("refusal published unattached commit: %s", got)
	}
}

//nolint:paralleltest // The native Git and read-only hosted receipt fixtures pin process-wide environment.
func TestE2ELifecycleNextDeletedTargetWithHeadAtDefaultBranchTipIsProvedDespiteAnUncontainedReceipt(t *testing.T) {
	fixture := newGitFixture(t)
	defaultHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "commit", "--allow-empty", "-m", "unattached local receipt commit")
	merge := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	installMergedPullRequestFixtureWithMerge(t, defaultHead, merge, time.Now().UTC())
	inspection := lifecycleInspection{ctx: context.Background(), home: fixture.home, canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app", base: "feature/test", head: defaultHead, withGitHub: true}
	err := inspection.checkGitHubIntegration()
	// The receipt is still refused: it contributes neither the absorption nor
	// the landing commit. Only ancestry of the head itself is the proof.
	requireDeletedTargetContainment(t, &inspection, err, defaultHead, "feature/test")
	if got := gitTestOutput(t, fixture.remote, "rev-parse", "HEAD"); got != defaultHead {
		t.Fatalf("inspection published the unattached commit: %s", got)
	}
}

//nolint:paralleltest // The native Git and hosted-observation fixtures pin process-wide environment; only owned Git objects are renamed.
func TestE2ELifecycleNextResidueEvidenceRetainsNativeObjectRefusal(t *testing.T) {
	fixture := newGitFixture(t)
	installPullRequestResponses(t, "[]", "")
	target := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "commit", "--allow-empty", "-m", "unpushed native residue")
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	if head == target {
		t.Fatal("native residue prerequisite did not advance HEAD")
	}
	if contained, err := isAncestor(context.Background(), fixture.canonical, head, target); err != nil || contained {
		t.Fatalf("native unpushed prerequisite=%v %v", contained, err)
	}
	objects := filepath.Join(fixture.canonical, ".git", "objects")
	retained := objects + ".retained"
	mutated := false
	negativeAncestry := 0
	nativeOutput := ""
	t.Cleanup(func() {
		if mutated {
			if err := os.Rename(retained, objects); err != nil {
				t.Error(err)
			}
		}
	})
	observer := &lifecycleNextNativeGitObservation{Runner: runner.New(), after: func(_ string, args []string, result runner.Result, err error) {
		if err != nil && result.ExitCode == 1 && reflect.DeepEqual(args, []string{"-C", fixture.canonical, "merge-base", "--is-ancestor", head, target}) {
			negativeAncestry++
			if negativeAncestry == 2 {
				if err := os.Rename(objects, retained); err != nil {
					t.Fatal(err)
				}
				mutated = true
			}
		} else if mutated && err != nil && len(args) > 2 && args[2] == "rev-list" {
			nativeOutput = strings.TrimSpace(result.CombinedOutput)
		}
	}}
	inspection := lifecycleInspection{ctx: withGitRunner(context.Background(), observer), home: fixture.home, canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app", base: "main", head: head, withGitHub: true, policy: inspectPolicy{residueEvidence: true, residueDepth: 10}}
	err := inspection.checkGitHubIntegration()
	if !mutated || negativeAncestry != 2 || nativeOutput == "" || err == nil || !strings.Contains(err.Error(), "list commits of "+head+" not in "+target) || !strings.Contains(err.Error(), nativeOutput) || inspection.result.IntegratedAtOrigin || inspection.result.AbsorbedAtOrigin || inspection.result.Landing != nil {
		t.Fatalf("native residue refusal=%+v %v negatives=%d native=%q", inspection.result, err, negativeAncestry, nativeOutput)
	}
	if err := os.Rename(retained, objects); err != nil {
		t.Fatal(err)
	}
	mutated = false
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
		t.Fatalf("refusal changed unpushed HEAD: %s", got)
	}
	if got := gitTestOutput(t, fixture.remote, "rev-parse", "HEAD"); got != target {
		t.Fatalf("refusal published unpushed residue: %s", got)
	}
}
