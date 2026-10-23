//go:build e2e

package orchestrate

import (
	"context"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // actual native Git fixture plus controlled external hosted PR/workflow observations mutate process PATH/XDG; no native custody or CI execution is simulated
func TestE2ELandOwnerDirectCIRefusalsUseActualNativeTargetAndInputIdentity(t *testing.T) {
	for _, stage := range []string{"fresh PR drift before push", "native target descendant after push"} {
		//nolint:paralleltest // Process-wide environment changes in TestE2ELandOwnerDirectCIRefusalsUseActualNativeTargetAndInputIdentity, installWorktreeMergeDirectGH, landFinalInstallNativeDirectCIContractGH; these rows share their parent environment and remain sequential.
		t.Run(stage, func(t *testing.T) {
			fixture := newExplicitRootEngineFixture(t)
			source := createMergeSource(t, fixture, "land-final-direct-ci", "feature/land-final-direct-ci", "land-final-direct-ci.txt", "actual native direct CI input-preserving source\n")
			target := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main"))
			runEngineGit(t, fixture.repository.CloneURL, "update-ref", "refs/heads/release", target)
			landFinalInstallNativeDirectCIContractGH(t, fixture.repository.CloneURL)
			receipt, prepareErr := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test", Route: WorktreeMergeRouteDirect, DirectCIPullRequest: "17", Timeout: 5 * time.Second})
			if prepareErr != nil || receipt.Status != WorktreeMergePrepared || receipt.Validation.Status != quality.StatusSkipped || receipt.ValidationDeferral == nil || receipt.ValidationDeferral.Route != WorktreeMergeRouteDirect || receipt.ValidationDeferral.CandidateSHA != receipt.Candidate.SHA {
				t.Fatalf("actual direct-CI preparation did not establish native skipped deferral: %+v %v", receipt, prepareErr)
			}
			if diff := strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "diff", "--name-only", receipt.TargetSHA, receipt.Candidate.SHA, "--", ".github", ".wb", "cmd/wb", "internal/quality", "go.mod", "go.sum")); diff != "" {
				t.Fatalf("actual native candidate changed prior CI inputs: %s", diff)
			}
			observed := false
			descendant := ""
			phase := ""
			options := WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteDirect, DirectCIPullRequest: "17", Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond, WaitSlice: 5 * time.Second, Progress: func(e progress.Event) {
				phase = e.Phase
				if stage == "fresh PR drift before push" && e.Phase == "resolve_route" && e.State == progress.Completed {
					t.Setenv("WB_TEST_DIRECT_CI_DRIFT", "1")
					observed = true
				}
				if stage == "native target descendant after push" && e.Phase == "publish_target" && e.State == progress.Completed && !observed {
					clone := filepath.Join(t.TempDir(), "owned-post-push-clone")
					runEngineGit(t, fixture.githubDir, "clone", fixture.repository.CloneURL, clone)
					runEngineGit(t, clone, "config", "user.name", "WB Test")
					runEngineGit(t, clone, "config", "user.email", "wb@example.test")
					runEngineGit(t, clone, "checkout", "main")
					writeEngineFile(t, filepath.Join(clone, "owned-post-push.txt"), "real later target descendant\n")
					runEngineGit(t, clone, "add", "owned-post-push.txt")
					runEngineGit(t, clone, "commit", "-m", "test: actual target advanced after direct publication")
					descendant = strings.TrimSpace(runEngineGit(t, clone, "rev-parse", "HEAD"))
					runEngineGit(t, clone, "push", "origin", "main")
					observed = true
				}
			}}
			got, runErr := LandWorktreeMerge(context.Background(), options)
			if !observed || runErr == nil || got.Status != WorktreeMergeConflict || got.ValidationDeferral == nil || got.ValidationDeferral.Route != WorktreeMergeRouteDirect || got.ValidationDeferral.CandidateSHA != receipt.Candidate.SHA || got.Validation.Status != quality.StatusSkipped {
				t.Fatalf("actual direct-CI native premise/refusal=%+v %v observed=%v phase=%s", got, runErr, observed, phase)
			}
			wantRemote := receipt.TargetSHA
			if stage == "fresh PR drift before push" {
				if !strings.Contains(runErr.Error(), "direct CI pull request changed before push") || got.PushGate != nil {
					t.Fatalf("fresh controlled PR drift did not precede native push: %+v %v", got, runErr)
				}
			} else {
				wantRemote = descendant
				if !strings.Contains(runErr.Error(), "direct CI deferral requires exact remote target") || got.PushGate == nil || got.PushGate.PreviousRemoteSHA != receipt.TargetSHA {
					t.Fatalf("native exact-target CI refusal=%+v %v", got, runErr)
				}
				if ancestor := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "merge-base", receipt.Candidate.SHA, descendant)); ancestor != receipt.Candidate.SHA {
					t.Fatalf("actual later target does not contain published candidate: %s", ancestor)
				}
			}
			if remote := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/main")); remote != wantRemote {
				t.Fatalf("refusal rewrote native target: %s want %s", remote, wantRemote)
			}
			if release := strings.TrimSpace(runEngineGit(t, fixture.repository.CloneURL, "rev-parse", "refs/heads/release")); release != receipt.TargetSHA {
				t.Fatalf("controlled CI base changed native ref: %s", release)
			}
			stored, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
			if err != nil || stored.Status != got.Status || stored.Failure != runErr.Error() || stored.Candidate.SHA != receipt.Candidate.SHA || stored.ValidationDeferral == nil {
				t.Fatalf("actual durable refusal evidence=%+v %v", stored, err)
			}
			if got.LandingSHA != "" || got.CanonicalSync != "" || len(got.CleanedTasks) != 0 {
				t.Fatalf("exact CI refusal performed later landing: %+v", got)
			}
		})
	}
}
