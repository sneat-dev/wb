//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // original scripted provider setup changes PATH and XDG_STATE_HOME; each mutation root is private
func TestE2ELandOwnerNativePublicationRefusalsKeepOriginalRemoteHeads(t *testing.T) {
	for _, route := range []WorktreeMergeRoute{WorktreeMergeRouteDirect, WorktreeMergeRoutePullRequest} {
		for _, stage := range []string{"pre-push hook", "receive hook", "remote observation", "pre-push save"} {
			//nolint:paralleltest // Process-wide environment changes in TestE2ELandOwnerNativePublicationRefusalsKeepOriginalRemoteHeads, installWorktreeMergeDirectGH; these rows share their parent environment and remain sequential.
			t.Run(string(route)+"/"+stage, func(t *testing.T) {
				fixture, receipt := landOwnerNativeFixture(t)
				installWorktreeMergeDirectGH(t)
				t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
				sentinel := errors.New("selected publication save refusal")
				armed := false
				refused := false
				options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: route, StopBeforeMerge: route == WorktreeMergeRoutePullRequest, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, Progress: func(e progress.Event) {
					if e.Phase != "pre_push_gate" || e.State != progress.Started {
						return
					}
					armed = true
					switch stage {
					case "pre-push hook":
						hooks := t.TempDir()
						if err := testenv.WriteExecutableFile(filepath.Join(hooks, "pre-push"), []byte("#!/bin/sh\necho native-pre-push-refusal >&2\nexit 1\n"), 0755); err != nil {
							t.Fatal(err)
						}
						runEngineGit(t, receipt.Candidate.Worktree, "config", "core.hooksPath", hooks)
					case "receive hook":
						if err := testenv.WriteExecutableFile(filepath.Join(fixture.repository.CloneURL, "hooks", "pre-receive"), []byte("#!/bin/sh\necho native-receive-refusal >&2\nexit 1\n"), 0755); err != nil {
							t.Fatal(err)
						}
					case "remote observation":
						runEngineGit(t, receipt.Candidate.Worktree, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "absent-owned-remote"))
					}
				}}
				durable, err := os.ReadFile(receipt.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				got, runErr := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
					if stage == "pre-push save" && armed && r.PushGate != nil {
						refused = true
						return sentinel
					}
					if err := persistWorktreeMergeReceipt(r); err != nil {
						return err
					}
					durable, err = os.ReadFile(r.ReceiptPath)
					return err
				})
				if !armed || runErr == nil {
					t.Fatalf("%s publication boundary not reached: %+v %v", stage, got, runErr)
				}
				if stage == "pre-push save" {
					if !refused || !errors.Is(runErr, sentinel) {
						t.Fatalf("pre-push save identity lost: %+v %v", got, runErr)
					}
				} else if got.Status != WorktreeMergeConflict {
					t.Fatalf("native refusal status=%s: %v", got.Status, runErr)
				}
				if stage == "pre-push hook" && !strings.Contains(runErr.Error(), "managed pre-push gate") {
					t.Fatalf("wrong actual hook boundary: %v", runErr)
				}
				if stage == "receive hook" && !strings.Contains(runErr.Error(), "push failed without force") {
					t.Fatalf("wrong actual receive boundary: %v", runErr)
				}
				if stage == "remote observation" && !strings.Contains(runErr.Error(), "inspect exact remote ref") {
					t.Fatalf("wrong remote observation boundary: %v", runErr)
				}
				if target := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); target != receipt.TargetSHA {
					t.Fatalf("failed publication changed target: %s", target)
				}
				if branch := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "for-each-ref", "--format=%(objectname)", "refs/heads/"+receipt.Candidate.Branch)); branch != "" {
					t.Fatalf("failed publication created candidate remote ref: %s", branch)
				}
				after, readErr := os.ReadFile(receipt.ReceiptPath)
				if readErr != nil || string(after) != string(durable) {
					t.Fatalf("native refusal changed last genuine durable checkpoint: %v", readErr)
				}
			})
		}
	}
}

//nolint:paralleltest // controlled provider subprocess errors use original PATH/XDG fixtures, real Git publication remains native
func TestE2ELandOwnerFreshPullRequestPublicationRefusalsRetainNativeCandidate(t *testing.T) {
	for _, stage := range []string{"published record save", "open discovery", "create observation", "created PR save", "handoff verification", "handoff save"} {
		//nolint:paralleltest // Process-wide environment changes in TestE2ELandOwnerFreshPullRequestPublicationRefusalsRetainNativeCandidate, installWorktreeMergePublishOnlyPRGH; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			installWorktreeMergePublishOnlyPRGH(t)
			t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
			t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
			t.Setenv("WB_TEST_GH_LOG", filepath.Join(t.TempDir(), "gh.log"))
			marker := ""
			switch stage {
			case "open discovery":
				marker = landOwnerPrivateProviderRefusal(t, "'api --paginate repos/"+receipt.Repository+"/commits/"+receipt.Candidate.SHA+"/pulls'")
			case "create observation":
				marker = landOwnerPrivateProviderRefusal(t, "'pr create --base main --head '*")
			case "handoff verification":
				marker = landOwnerPrivateProviderRefusal(t, "'pr view '*' --json state,headRefOid,baseRefName'")
			}
			sentinel := errors.New("selected fresh PR save refusal")
			phase := ""
			refused := false
			durable, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, Progress: func(e progress.Event) { phase = e.Phase }}
			got, runErr := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
				selected := stage == "published record save" && phase == "publish_candidate" && r.PublishedCandidateSHA != "" && r.PullRequest == "" || stage == "created PR save" && phase == "open_pull_request" && r.PullRequest != "" || stage == "handoff save" && r.Status == WorktreeMergePublished
				if selected {
					refused = true
					return sentinel
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					return err
				}
				durable, err = os.ReadFile(r.ReceiptPath)
				return err
			})
			if runErr == nil {
				t.Fatalf("fresh PR fault not reached: %+v", got)
			}
			if stage == "open discovery" && !strings.Contains(runErr.Error(), "query pull requests for exact candidate "+receipt.Candidate.SHA) {
				t.Fatalf("wrong propagating exact-head discovery boundary: %v", runErr)
			}
			if marker != "" {
				if _, err := os.Stat(marker); err != nil || !strings.Contains(runErr.Error(), "controlled late provider refusal") {
					t.Fatalf("actual controlled provider error not observed: %v %v", runErr, err)
				}
			} else if !refused || !errors.Is(runErr, sentinel) {
				t.Fatalf("chosen Save identity absent: %+v %v", got, runErr)
			}
			remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch))
			if remote != receipt.Candidate.SHA {
				t.Fatalf("actual publication was not retained after later refusal: %s", remote)
			}
			if target := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); target != receipt.TargetSHA || got.LandingSHA != "" {
				t.Fatalf("PR refusal landed target: %s %+v", target, got)
			}
			after, readErr := os.ReadFile(receipt.ReceiptPath)
			if readErr != nil || string(after) != string(durable) {
				t.Fatalf("late refusal changed last real persisted checkpoint: %v", readErr)
			}
		})
	}
}

//nolint:paralleltest // native landing uses the original controlled provider PATH/XDG fixture
func TestE2ELandOwnerFreshCanonicalAndCleanupFaultsPreserveExactLanding(t *testing.T) {
	for _, stage := range []string{"canonical dirty", "canonical missing origin", "canonical missing path", "cleanup dirty source", "cleanup malformed claim"} {
		//nolint:paralleltest // Process-wide environment changes in TestE2ELandOwnerFreshCanonicalAndCleanupFaultsPreserveExactLanding, installWorktreeMergeDirectGH; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
			armed := false
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteDirect, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, Cleanup: strings.HasPrefix(stage, "cleanup"), Progress: func(e progress.Event) {
				if strings.HasPrefix(stage, "canonical") && e.Phase == "sync_canonical" && e.State == progress.Started {
					armed = true
					switch stage {
					case "canonical dirty":
						writeEngineFile(t, filepath.Join(fixture.canonical, "owned-unsaved.txt"), "private dirty canonical\n")
					case "canonical missing origin":
						runEngineGit(t, fixture.canonical, "remote", "remove", "origin")
					case "canonical missing path":
						if err := os.Rename(fixture.canonical, fixture.canonical+".owned-moved"); err != nil {
							t.Fatal(err)
						}
					}
				}
				if strings.HasPrefix(stage, "cleanup") && e.Phase == "cleanup" && e.State == progress.Started {
					armed = true
					switch stage {
					case "cleanup dirty source":
						writeEngineFile(t, filepath.Join(receipt.Sources[0].Worktree, "owned-unsaved.txt"), "private dirty source\n")
					case "cleanup malformed claim":
						view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Sources[0].Worktree})
						if err != nil || view.Claim == nil {
							t.Fatalf("actual native source claim missing before cleanup fault: %+v %v", view, err)
						}
						if err := os.WriteFile(view.Claim.ClaimPath, []byte("{broken"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}}
			got, runErr := LandWorktreeMerge(context.Background(), options)
			if !armed || runErr == nil || got.LandingSHA == "" {
				t.Fatalf("fresh native %s fault not reached: %+v %v", stage, got, runErr)
			}
			want := WorktreeMergeCanonicalSyncBlocked
			if strings.HasPrefix(stage, "cleanup") {
				want = WorktreeMergeLanded
			}
			if got.Status != want {
				t.Fatalf("actual failure status=%s want %s: %v", got.Status, want, runErr)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != got.LandingSHA {
				t.Fatalf("late fault lost real remote landing: %s %+v", remote, got)
			}
			stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if err != nil || stored.Failure != runErr.Error() || stored.LandingSHA != got.LandingSHA || stored.Status != want {
				t.Fatalf("late native refusal not durable: %+v %v", stored, err)
			}
			lock, err := AcquireOperationLock(fixture.githubDir, receipt.Lane, true)
			if err != nil {
				t.Fatalf("late fault retained owned lock: %v", err)
			}
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

//nolint:paralleltest // exact provider state is controlled through original process-wide PATH/XDG fixture
func TestE2ELandOwnerFreshTargetCheckFaultsPreserveActualRemoteLanding(t *testing.T) {
	for _, stage := range []string{"pending", "failed", "failed with revert"} {
		//nolint:paralleltest // Process-wide environment changes in TestE2ELandOwnerFreshTargetCheckFaultsPreserveActualRemoteLanding, installWorktreeMergeDirectGH, installWorktreeMergeEngineGH; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
			armed := false
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteDirect, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second}
			if stage == "pending" {
				options.WaitSlice = 20 * time.Millisecond
			}
			if stage == "failed with revert" {
				options.OnFailure = "revert"
			}
			options.Progress = func(e progress.Event) {
				if e.Phase == "publish_target" && e.State == progress.Completed {
					armed = true
					gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
					conclusion := "failure"
					if stage == "pending" {
						conclusion = ""
					}
					gh.writeState(t, "check-conclusion", conclusion)
				}
			}
			got, err := LandWorktreeMerge(context.Background(), options)
			want := WorktreeMergePostTargetCIFailed
			if stage == "pending" {
				want = WorktreeMergeChecksPending
			}
			if !armed || err == nil || got.Status != want || got.CanonicalSync != "" {
				t.Fatalf("fresh checks=%+v %v armed=%v want %s", got, err, armed, want)
			}
			if stage != "pending" {
				found := false
				for _, check := range got.Checks.Checks {
					found = found || check.Name == "check-run:CI" && check.Bucket == "fail" && check.Conclusion == "failure"
				}
				if !found {
					t.Fatalf("actual completed failing provider observation absent: %+v", got.Checks)
				}
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != receipt.Candidate.SHA || got.LandingSHA != remote {
				t.Fatalf("check failure lost native landing: %s %+v", remote, got)
			}
			stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if stage != "failed with revert" {
				if readErr != nil || stored.Status != want || stored.Failure != err.Error() {
					t.Fatalf("fresh check refusal not durable: %+v %v", stored, readErr)
				}
				return
			}
			// The native forward-revert preparation replaces this same receipt;
			// the returned landing value retains the original CI failure.
			if readErr != nil || stored.Phase != WorktreeMergePhaseRevert || stored.Status != WorktreeMergePrepared || stored.Failure != "" || stored.RevertOf == nil {
				t.Fatalf("native forward revert was not durably prepared: %+v %v", stored, readErr)
			}
			if got.PreviousTargetSHA == "" || stored.RevertOf.PreviousTargetSHA != got.PreviousTargetSHA || stored.RevertOf.LandingSHA != got.LandingSHA || stored.RevertOf.CandidateSHA != got.Candidate.SHA || stored.TargetSHA != got.LandingSHA {
				t.Fatalf("forward revert lost exact landed before/after identities: %+v original=%+v", stored, got)
			}
			if stored.Candidate.Task != "revert-"+receipt.ID || stored.Candidate.Branch != "wb/revert/"+receipt.ID || stored.Candidate.Worktree == receipt.Candidate.Worktree || stored.Candidate.SHA == "" || stored.Candidate.SHA == receipt.Candidate.SHA {
				t.Fatalf("forward revert candidate lacks genuine new identity: %+v", stored.Candidate)
			}
			if _, guardErr := worktrees.Guard(context.Background(), stored.Candidate.Worktree, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: receipt.Target}); guardErr != nil {
				t.Fatalf("native forward revert candidate guard: %v", guardErr)
			}
			if head := strings.TrimSpace(runEngineGit(t, stored.Candidate.Worktree, "rev-parse", "HEAD")); head != stored.Candidate.SHA {
				t.Fatalf("forward revert recorded HEAD differs from native candidate: %s %+v", head, stored.Candidate)
			}
			originalTree := strings.TrimSpace(runEngineGit(t, stored.Candidate.Worktree, "rev-parse", got.PreviousTargetSHA+"^{tree}"))
			revertedTree := strings.TrimSpace(runEngineGit(t, stored.Candidate.Worktree, "rev-parse", stored.Candidate.SHA+"^{tree}"))
			if originalTree != revertedTree {
				t.Fatalf("forward revert did not actually reverse the landed tree: before=%s reverted=%s", originalTree, revertedTree)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != got.LandingSHA {
				t.Fatalf("preparing the forward revert changed the landed remote: %s want %s", remote, got.LandingSHA)
			}
		})
	}
}

//nolint:paralleltest // controlled original provider observations use PATH/XDG; Runner fault is per invocation only
func TestE2ELandOwnerPostNativePushFetchAndAncestryFaultsRetainRemoteCommit(t *testing.T) {
	for _, stage := range []string{"fetch", "remote revision", "ancestry", "native rewind"} {
		//nolint:paralleltest // Process-wide environment changes in TestE2ELandOwnerPostNativePushFetchAndAncestryFaultsRetainRemoteCommit, installWorktreeMergeDirectGH; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			installWorktreeMergeDirectGH(t)
			t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
			armed := false
			sentinel := errors.New("selected post-native-push " + stage + " refusal")
			run := &landOwnerFaultRunner{Runner: defaultRunner, failure: sentinel, refuse: func(dir, name string, args []string) bool {
				if !armed || dir != receipt.Candidate.Worktree || name != "git" {
					return false
				}
				switch stage {
				case "fetch":
					return reflect.DeepEqual(args, []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"})
				case "remote revision":
					return reflect.DeepEqual(args, []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"})
				case "ancestry":
					return reflect.DeepEqual(args, []string{"merge-base", receipt.Candidate.SHA, receipt.Candidate.SHA})
				}
				return false
			}}
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteDirect, run: run, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, Progress: func(e progress.Event) {
				if e.Phase == "publish_target" && e.State == progress.Completed {
					armed = true
					if stage == "native rewind" {
						runEngineGit(t, fixture.repository.CloneURL, "update-ref", "refs/heads/main", receipt.TargetSHA, receipt.Candidate.SHA)
					}
				}
			}}
			got, err := LandWorktreeMerge(context.Background(), options)
			if !armed || err == nil || got.Status != WorktreeMergeConflict {
				t.Fatalf("post push %s refusal=%+v %v", stage, got, err)
			}
			if stage != "native rewind" {
				if !run.refused || !errors.Is(err, sentinel) || run.later != 0 {
					t.Fatalf("exact post-push command fault lost: %v calls=%v", err, run.calls)
				}
			} else if !strings.Contains(err.Error(), "does not contain server landing") {
				t.Fatalf("native rewind did not reach exact containment guard: %v", err)
			}
			want := receipt.Candidate.SHA
			if stage == "native rewind" {
				want = receipt.TargetSHA
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != want {
				t.Fatalf("postpush refusal unexpectedly rewrote owned remote: %s", remote)
			}
			if got.CanonicalSync != "" || len(got.CleanedTasks) != 0 {
				t.Fatalf("refused verification advanced later effects: %+v", got)
			}
			stored, readErr := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if readErr != nil || stored.Failure != err.Error() || stored.Status != got.Status {
				t.Fatalf("postpush refusal not durable: %+v %v", stored, readErr)
			}
		})
	}
}

//nolint:paralleltest // genuine native published branch plus original scripted provider PATH/XDG observations
func TestE2ELandOwnerPublishedDescendantRecoveryFaultsKeepOriginalPublication(t *testing.T) {
	for _, stage := range []string{"HEAD observation", "transient HEAD observation", "published predecessor ancestry", "local descendant ancestry", "advanced receipt save", "unrelated recorded predecessor"} {
		//nolint:paralleltest // Process-wide environment changes in installWorktreeMergeEngineGH; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/"+receipt.Candidate.Branch)
			gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
			originalPublished := receipt.Candidate.SHA
			receipt.PullRequest, receipt.PublishedCandidateSHA = gh.pr, originalPublished
			receipt.Status = WorktreeMergePublished
			writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "owned-recovery-descendant.txt"), "real owned descendant\n")
			runEngineGit(t, receipt.Candidate.Worktree, "add", "owned-recovery-descendant.txt")
			runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: native unpublished descendant before recovery fault")
			descendant := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
			if stage == "unrelated recorded predecessor" {
				writeEngineFile(t, filepath.Join(fixture.canonical, "independent-predecessor.txt"), "different native lineage\n")
				runEngineGit(t, fixture.canonical, "add", "independent-predecessor.txt")
				runEngineGit(t, fixture.canonical, "commit", "-m", "test: independent native predecessor refusal")
				receipt.PublishedCandidateSHA = strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
			}
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			durable, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("selected published descendant observation refusal")
			cause := sentinel
			if stage == "transient HEAD observation" {
				cause = githubobserver.ErrTransientMutationOutcomeUnknown
			}
			run := &landOwnerFaultRunner{Runner: defaultRunner, failure: cause, refuse: func(dir, name string, args []string) bool {
				if dir != receipt.Candidate.Worktree || name != "git" {
					return false
				}
				switch stage {
				case "HEAD observation", "transient HEAD observation":
					return reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"})
				case "published predecessor ancestry":
					return reflect.DeepEqual(args, []string{"merge-base", originalPublished, receipt.Candidate.SHA})
				case "local descendant ancestry":
					return reflect.DeepEqual(args, []string{"merge-base", receipt.Candidate.SHA, descendant})
				}
				return false
			}}
			refusedSave := false
			got, runErr := landWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, run: run}, func(r WorktreeMergeReceipt) error {
				if stage == "advanced receipt save" && r.Candidate.SHA == descendant {
					refusedSave = true
					return sentinel
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					return err
				}
				durable, err = os.ReadFile(r.ReceiptPath)
				return err
			})
			if runErr == nil {
				t.Fatalf("published recovery %s did not refuse: %+v", stage, got)
			}
			switch stage {
			case "advanced receipt save":
				if !refusedSave || !errors.Is(runErr, sentinel) || got.Candidate.SHA != descendant {
					t.Fatalf("advanced persistence fault missing: %+v %v", got, runErr)
				}
			case "unrelated recorded predecessor":
				if !strings.Contains(runErr.Error(), "does not descend from published candidate") || got.Status != WorktreeMergeConflict {
					t.Fatalf("real native predecessor guard not reached: %+v %v", got, runErr)
				}
			default:
				want := WorktreeMergeConflict
				if stage == "transient HEAD observation" {
					want = WorktreeMergeChecksPending
				}
				if !run.refused || !errors.Is(runErr, cause) || run.later != 0 || got.Status != want {
					t.Fatalf("controlled negative observation classification=%+v %v calls=%v", got, runErr, run.calls)
				}
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != originalPublished {
				t.Fatalf("recovery failure published descendant: %s", remote)
			}
			if head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); head != descendant {
				t.Fatalf("recovery failure modified actual local descendant: %s", head)
			}
			if got.LandingSHA != "" || len(got.CleanedTasks) != 0 {
				t.Fatalf("negative recovery performed later landing/cleanup: %+v", got)
			}
			after, readErr := os.ReadFile(receipt.ReceiptPath)
			if readErr != nil || string(after) != string(durable) {
				t.Fatalf("negative recovery changed last real durable bytes: %v", readErr)
			}
		})
	}
}
