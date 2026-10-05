package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // original scripted provider uses private state through process-wide PATH/XDG fixture
func TestLandOwnerStopBeforeDescendantPublicationPreservesExactNativeCheckpoints(t *testing.T) {
	for _, stage := range []string{"success", "pre-push hook", "published predecessor drift", "push rejection", "gate save", "published SHA save", "handoff save", "handoff observation"} {
		t.Run(stage, func(t *testing.T) {
			fixture, receipt := landOwnerNativeFixture(t)
			original := receipt.Candidate.SHA
			runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", original+":refs/heads/"+receipt.Candidate.Branch)
			gh := installWorktreeMergeEngineGH(t, fixture, original, receipt.Candidate.Branch)
			receipt.PullRequest, receipt.PublishedCandidateSHA = gh.pr, original
			receipt.Status = WorktreeMergePublished
			writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "final-descendant.txt"), "actual private unpublished descendant\n")
			runEngineGit(t, receipt.Candidate.Worktree, "add", "final-descendant.txt")
			runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: exact native descendant for intentional handoff")
			descendant := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			durable, err := os.ReadFile(receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("selected descendant " + stage + " save refusal")
			phase := ""
			armed := false
			refused := false
			marker := ""
			descendantWrites := 0
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, StopBeforeMerge: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second, Progress: func(e progress.Event) {
				phase = e.Phase
				if e.Phase == "pre_push_gate" && e.State == progress.Started {
					armed = true
					switch stage {
					case "pre-push hook":
						hooks := t.TempDir()
						writeEngineFile(t, filepath.Join(hooks, "pre-push"), "#!/bin/sh\necho owned-descendant-pre-push-refusal >&2\nexit 1\n")
						if err := os.Chmod(filepath.Join(hooks, "pre-push"), 0o755); err != nil {
							t.Fatal(err)
						}
						runEngineGit(t, receipt.Candidate.Worktree, "config", "core.hooksPath", hooks)
					case "published predecessor drift":
						runEngineGit(t, fixture.repository.CloneURL, "update-ref", "refs/heads/"+receipt.Candidate.Branch, receipt.TargetSHA, original)
					case "push rejection":
						hook := filepath.Join(fixture.repository.CloneURL, "hooks", "pre-receive")
						writeEngineFile(t, hook, "#!/bin/sh\necho owned-descendant-receive-refusal >&2\nexit 1\n")
						if err := os.Chmod(hook, 0o755); err != nil {
							t.Fatal(err)
						}
					}
				}
				if e.Phase == "publish_candidate" && e.State == progress.Completed && stage == "handoff observation" {
					marker = landOwnerPrivateProviderRefusal(t, "'pr view '*\" --json state,headRefOid,baseRefName\"")
				}
			}}
			got, runErr := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
				if phase == "publish_candidate" && r.PublishedCandidateSHA == descendant {
					descendantWrites++
				}
				selected := stage == "gate save" && phase == "pre_push_gate" && r.PushGate != nil || stage == "published SHA save" && descendantWrites == 1 || stage == "handoff save" && descendantWrites == 2
				if selected && !refused {
					refused = true
					return sentinel
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					return err
				}
				durable, err = os.ReadFile(r.ReceiptPath)
				return err
			})
			if !armed {
				t.Fatalf("descendant republish boundary not reached: %+v %v", got, runErr)
			}
			if stage == "success" {
				if runErr != nil || got.Status != WorktreeMergePublished || got.PublishedCandidateSHA != descendant {
					t.Fatalf("actual descendant handoff=%+v %v", got, runErr)
				}
			} else if runErr == nil {
				t.Fatalf("descendant %s refusal absent: %+v", stage, got)
			}
			if strings.HasSuffix(stage, "save") && (!refused || !errors.Is(runErr, sentinel)) {
				t.Fatalf("selected checkpoint identity=%v refused=%v", runErr, refused)
			}
			if stage == "handoff observation" {
				if marker == "" {
					t.Fatal("late actual provider observation not armed")
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("exact late observation not executed: %v", err)
				}
			}
			wantRemote := original
			if stage == "success" || stage == "published SHA save" || stage == "handoff save" || stage == "handoff observation" {
				wantRemote = descendant
			}
			if stage == "published predecessor drift" {
				wantRemote = receipt.TargetSHA
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/"+receipt.Candidate.Branch)); remote != wantRemote {
				t.Fatalf("native branch publication=%s want %s", remote, wantRemote)
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != receipt.TargetSHA {
				t.Fatalf("intentional handoff changed target: %s", remote)
			}
			if current, err := os.ReadFile(receipt.ReceiptPath); err != nil || string(current) != string(durable) {
				t.Fatalf("selected save replaced actual durable checkpoint: %v", err)
			}
			if got.LandingSHA != "" || got.CanonicalSync != "" || len(got.CleanedTasks) != 0 {
				t.Fatalf("handoff advanced landing effects: %+v", got)
			}
			if head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); head != descendant {
				t.Fatalf("native descendant candidate changed: %s", head)
			}
		})
	}
}

//nolint:paralleltest // actual original provider PATH/XDG state; only the owned Unix release row renames an open lock inode
func TestLandOwnerObservedMergedPRRefusalsUseExistingNativeCandidate(t *testing.T) {
	for _, stage := range []string{"intent save", "stranded observation", "landing observation", "fetch", "ancestry error", "native missing containment", "landed save", "owned release"} {
		t.Run(stage, func(t *testing.T) {
			if stage == "owned release" && runtime.GOOS == "windows" {
				t.Skip("Windows cannot rename the open owned lock inode; actual Unix release refusal")
			}
			fixture, receipt := landOwnerNativeFixture(t)
			runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/"+receipt.Candidate.Branch)
			gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
			receipt.PullRequest, receipt.PublishedCandidateSHA = gh.pr, receipt.Candidate.SHA
			receipt.Status = WorktreeMergePublished
			if stage == "stranded observation" {
				receipt.Phase = WorktreeMergePhaseLand
			}
			if err := persistWorktreeMergeReceipt(receipt); err != nil {
				t.Fatal(err)
			}
			originalTarget := receipt.TargetSHA
			if stage == "stranded observation" {
				if err := os.Rename(receipt.Candidate.Worktree, receipt.Candidate.Worktree+".owned-missing"); err != nil {
					t.Fatal(err)
				}
			} else if stage != "intent save" && stage != "landing observation" {
				runEngineGit(t, fixture.repository.CloneURL, "update-ref", "refs/heads/main", receipt.Candidate.SHA, originalTarget)
				gh.writeState(t, "pr-state", "CLOSED")
				gh.writeState(t, "merged", "true")
			}
			marker := ""
			if stage == "landing observation" {
				marker = landOwnerPrivateProviderRefusal(t, "'pr view '*\" --json state,mergedAt,mergeCommit,headRefOid,baseRefName\"")
			}
			sentinel := errors.New("selected observed-merged " + stage + " refusal")
			rewound := false
			run := &landOwnerFaultRunner{Runner: defaultRunner, failure: sentinel, refuse: func(dir, name string, args []string) bool {
				if dir != receipt.Candidate.Worktree || name != "git" {
					return false
				}
				fetch := reflect.DeepEqual(args, []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"})
				if stage == "native missing containment" && fetch && !rewound {
					runEngineGit(t, fixture.repository.CloneURL, "update-ref", "refs/heads/main", originalTarget, receipt.Candidate.SHA)
					rewound = true
				}
				return stage == "fetch" && fetch || stage == "ancestry error" && reflect.DeepEqual(args, []string{"merge-base", receipt.Candidate.SHA, receipt.Candidate.SHA})
			}}
			home, err := wbhome.Root(fixture.githubDir)
			if err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(home, "worktrees", receipt.Lane, ".lock")
			successor := []byte("operation=owned-merged-successor\npid=1\n")
			replaced := false
			selected := false
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRoutePullRequest, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second, run: run}
			got, runErr := landWorktreeMerge(context.Background(), options, func(r WorktreeMergeReceipt) error {
				choose := stage == "intent save" && r.OnFailure == "stop" && len(r.ResumeArgs) > 0 || stage == "landed save" && r.Status == WorktreeMergeLanded && r.LandingSHA == receipt.Candidate.SHA
				if choose && !selected {
					selected = true
					return sentinel
				}
				if err := persistWorktreeMergeReceipt(r); err != nil {
					return err
				}
				if stage == "owned release" && !replaced && r.Status == WorktreeMergeLanded && r.LandingSHA == receipt.Candidate.SHA {
					if err := os.Rename(lockPath, lockPath+".owned-held"); err != nil {
						return err
					}
					if err := os.WriteFile(lockPath, successor, 0o600); err != nil {
						return err
					}
					replaced = true
				}
				return nil
			})
			if runErr == nil {
				t.Fatalf("selected merged %s refusal absent: %+v", stage, got)
			}
			if stage == "intent save" || stage == "landed save" {
				if !selected || !errors.Is(runErr, sentinel) {
					t.Fatalf("actual selected save identity lost: %v", runErr)
				}
			}
			if stage == "fetch" || stage == "ancestry error" {
				if !run.refused || !errors.Is(runErr, sentinel) || run.later != 0 {
					t.Fatalf("exact real-runner refusal=%v calls=%v", runErr, run.calls)
				}
			}
			if stage == "native missing containment" {
				if !rewound || !strings.Contains(runErr.Error(), "does not contain already-merged pull-request result") {
					t.Fatalf("actual native containment refusal=%v rewound=%v", runErr, rewound)
				}
			}
			if stage == "landing observation" {
				if _, err := os.Stat(marker); err != nil || !strings.Contains(runErr.Error(), "read pull-request landing receipt") {
					t.Fatalf("exact late landing observation=%v marker=%v", runErr, err)
				}
			}
			if stage == "stranded observation" && !strings.Contains(runErr.Error(), "not MERGED") {
				t.Fatalf("native missing candidate did not refuse open remote-only proof: %v", runErr)
			}
			if stage == "owned release" {
				if !replaced || !strings.Contains(runErr.Error(), "operation lock changed before retirement") {
					t.Fatalf("actual release refusal=%v replaced=%v", runErr, replaced)
				}
				if b, err := os.ReadFile(lockPath); err != nil || string(b) != string(successor) {
					t.Fatalf("successor lock mutated: %q %v", b, err)
				}
			}
			wantRemote := receipt.Candidate.SHA
			if stage == "intent save" || stage == "stranded observation" || stage == "landing observation" || stage == "native missing containment" {
				wantRemote = originalTarget
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != wantRemote {
				t.Fatalf("native remote moved after selected refusal: %s want %s", remote, wantRemote)
			}
			if got.CanonicalSync != "" || len(got.CleanedTasks) != 0 {
				t.Fatalf("refusal advanced canonical/cleanup: %+v", got)
			}
			if stage != "stranded observation" {
				if head := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD")); head != receipt.Candidate.SHA {
					t.Fatalf("existing native candidate changed: %s", head)
				}
			}
		})
	}
}
