package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
)

// landFinalInstallNativeDirectCIContractGH preserves the existing native
// provider's actual Git ref/ancestry observations. Only external PR/workflow
// metadata is scripted, with its head read from the owned bare repository.
func landFinalInstallNativeDirectCIContractGH(t *testing.T, remote string) {
	t.Helper()
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_REMOTE", remote)
	t.Setenv("WB_TEST_DIRECT_CI_DRIFT", "0")
	script := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	metadata := `case "$*" in
 'api repos/acme/app/pulls/17 --include'|'api repos/acme/app/pulls/17')
  head="$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)"
  state=open
  if [ "$WB_TEST_DIRECT_CI_DRIFT" = 1 ]; then state=closed; fi
  printf '{"number":17,"state":"%s","merged":false,"head":{"ref":"main","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"release","repo":{"full_name":"acme/app"}}}\n' "$state" "$head"; exit 0 ;;
 'api repos/acme/app/branches/release --include'|'api repos/acme/app/branches/release')
  printf '%s\n' '{"protected":true,"protection":{"required_status_checks":{"contexts":["Required checks passed"]}}}'; exit 0 ;;
 'api repos/acme/app/branches/release/protection/required_status_checks --include'|'api repos/acme/app/branches/release/protection/required_status_checks')
  printf '%s\n' '{"strict":true,"contexts":["Required checks passed"],"checks":[]}'; exit 0 ;;
 'api repos/acme/app/rules/branches/release?per_page=100 --include'|'api repos/acme/app/rules/branches/release?per_page=100')
  printf '%s\n' '[]'; exit 0 ;;
 'api repos/acme/app/actions/workflows/go-ci.yml --include'|'api repos/acme/app/actions/workflows/go-ci.yml')
  printf '%s\n' '{"id":300,"name":"Go CI","path":".github/workflows/go-ci.yml","state":"active"}'; exit 0 ;;
 'api repos/acme/app/actions/runs?head_sha='*'&per_page=100 --include'|'api repos/acme/app/actions/runs?head_sha='*'&per_page=100')
  head="$(git --git-dir="$WB_TEST_REMOTE" rev-parse refs/heads/main)"
  printf '{"total_count":1,"workflow_runs":[{"id":15,"workflow_id":300,"head_sha":"%s","head_branch":"main","event":"pull_request","status":"completed","conclusion":"success","created_at":"2026-09-27T09:00:00Z","check_suite_id":305,"pull_requests":[{"number":17,"base":{"ref":"release"}}]}]}\n' "$head"; exit 0 ;;
esac
`
	updated := strings.Replace(string(body), "#!/bin/sh\n", "#!/bin/sh\nset -eu\n"+metadata, 1)
	if updated == string(body) {
		t.Fatal("private provider insertion point missing")
	}
	if err := os.WriteFile(script, []byte(updated), 0755); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // actual native Git fixture plus controlled external hosted PR/workflow observations mutate process PATH/XDG; no native custody or CI execution is simulated
func TestLandOwnerDirectCIRefusalsUseActualNativeTargetAndInputIdentity(t *testing.T) {
	for _, stage := range []string{"fresh PR drift before push", "native target descendant after push"} {
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
