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

//nolint:paralleltest // The native Git and read-only hosted-observation fixtures pin process-wide environment.
func TestE2ELifecycleNextDeletedTargetRequiresExactMergedReceipt(t *testing.T) {
	fixture := newGitFixture(t)
	installPullRequestResponses(t, "[]", "")
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	target := strings.Fields(gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main"))
	if len(target) != 2 || target[0] != head {
		t.Fatalf("native remote control=%v", target)
	}
	if _, err := fetchRemoteTargetHead(context.Background(), fixture.canonical, "feature/deleted-target"); !isMissingRemoteTargetError(err) {
		t.Fatalf("native deleted-target prerequisite=%v", err)
	}
	inspection := lifecycleInspection{ctx: context.Background(), home: fixture.home, canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app", base: "feature/deleted-target", head: head, withGitHub: true}
	err := inspection.checkGitHubIntegration()
	if err == nil || !strings.Contains(err.Error(), "GitHub has no exact merged receipt into default branch main") || inspection.result.RemoteTargetSHA != target[0] || inspection.result.IntegratedAtOrigin || inspection.result.AbsorbedAtOrigin || inspection.result.MergedPullRequest != nil {
		t.Fatalf("absent exact receipt admitted=%+v %v", inspection.result, err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != head {
		t.Fatalf("refusal changed head: %s", got)
	}
}

//nolint:paralleltest // The native Git and read-only hosted receipt fixtures pin process-wide environment.
func TestE2ELifecycleNextDeletedTargetRejectsUncontainedMergeCommit(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "commit", "--allow-empty", "-m", "unattached local receipt commit")
	merge := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	if merge == head {
		t.Fatal("native uncontained commit prerequisite did not advance local HEAD")
	}
	if contained, err := isAncestor(context.Background(), fixture.canonical, merge, head); err != nil || contained {
		t.Fatalf("native uncontained commit prerequisite=%v %v", contained, err)
	}
	installMergedPullRequestFixtureWithMerge(t, head, merge, time.Now().UTC())
	inspection := lifecycleInspection{ctx: context.Background(), home: fixture.home, canonical: fixture.canonical, worktree: fixture.canonical, slug: "acme/app", base: "feature/test", head: head, withGitHub: true}
	err := inspection.checkGitHubIntegration()
	if err == nil || !strings.Contains(err.Error(), "merged receipt #17 commit "+merge+" is not contained in freshly fetched origin/main") || inspection.result.RemoteTargetSHA != head || inspection.result.IntegratedAtOrigin || inspection.result.MergedPullRequest != nil {
		t.Fatalf("uncontained receipt admitted=%+v %v", inspection.result, err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD"); got != merge {
		t.Fatalf("refusal changed retained unattached commit: %s", got)
	}
	if got := gitTestOutput(t, fixture.remote, "rev-parse", "HEAD"); got != head {
		t.Fatalf("refusal published unattached commit: %s", got)
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
