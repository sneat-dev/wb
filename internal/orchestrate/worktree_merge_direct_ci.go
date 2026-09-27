package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// The WB Go CI workflow's aggregate depends on the coverage job. Both must
// actually execute on the pushed head when their result replaces local suite
// validation; a generic green or empty direct-target check set is insufficient.
var directCIGoChecks = []string{"Required checks passed", "Tests and coverage (8 shards)"}

type worktreeMergeDirectCIContract struct {
	PullRequest       string
	PullRequestNumber int
	Base              string
	WorkflowID        int64
}

func resolveWorktreeMergeDirectCIContract(ctx context.Context, repository, target, pullRequest string) (*worktreeMergeDirectCIContract, error) {
	if strings.TrimSpace(pullRequest) == "" {
		return nil, fmt.Errorf("direct CI deferral requires an open pull request")
	}
	view, err := ReadPullRequest(ctx, repository, pullRequest)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(view.State, "open") || view.Merged || view.Head.Ref != target || view.Head.Repo == nil || view.Head.Repo.FullName != repository ||
		view.Base.Ref == "" || view.Base.Ref == target || view.Base.Repo == nil || view.Base.Repo.FullName != repository || view.Head.SHA == "" {
		return nil, fmt.Errorf("pull request %s is not an open same-repository %s head into a distinct base", pullRequest, target)
	}
	remoteHead, reason := targetHead(ctx, repository, target)
	if reason != "" {
		return nil, fmt.Errorf("read exact remote %s head for CI deferral: %s", target, reason)
	}
	if remoteHead != view.Head.SHA {
		return nil, fmt.Errorf("pull request %s head %s differs from remote %s at %s", pullRequest, view.Head.SHA, target, remoteHead)
	}
	checks, freshness, reason := targetBranchRequiredChecks(ctx, repository, view.Base.Ref, true)
	if reason != "" || freshness == "" || !hasRequiredRemoteCheck(checks, directCIGoChecks[0]) {
		return nil, fmt.Errorf("pull request base %s lacks an authoritative strict required Go CI aggregate: %s", view.Base.Ref, reason)
	}
	body, err := githubGet(ctx, "", repository, target, remoteHead, "repos/"+repository+"/actions/workflows/go-ci.yml")
	if err != nil {
		return nil, fmt.Errorf("read Go CI workflow: %w", err)
	}
	var workflow struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Path  string `json:"path"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &workflow); err != nil {
		return nil, fmt.Errorf("decode Go CI workflow: %w", err)
	}
	if workflow.ID <= 0 || workflow.Name != "Go CI" || workflow.Path != ".github/workflows/go-ci.yml" || workflow.State != "active" {
		return nil, fmt.Errorf("Go CI workflow identity is not active and exact")
	}
	_, priorRuns, reason := githubActionsRunsForHead(ctx, PullRequestWaitOptions{Repository: repository, Target: target, Head: remoteHead})
	if reason != "" {
		return nil, fmt.Errorf("read prior exact-head Actions runs: %s", reason)
	}
	for _, run := range priorRuns {
		if run.WorkflowID == workflow.ID && run.Event == "pull_request" && run.HeadSHA == remoteHead && run.HeadBranch == target && runIncludesPullRequest(run, view.Number, view.Base.Ref) && run.Conclusion == "success" {
			return &worktreeMergeDirectCIContract{PullRequest: pullRequest, PullRequestNumber: view.Number, Base: view.Base.Ref, WorkflowID: workflow.ID}, nil
		}
	}
	return nil, fmt.Errorf("Go CI has no pull_request run for exact current %s head %s", target, remoteHead)
}

func runIncludesPullRequest(run githubActionsRun, number int, base string) bool {
	for _, pr := range run.PullRequests {
		if pr.Number == number && pr.Base.Ref == base {
			return true
		}
	}
	return false
}

// The previous exact-head PR run is evidence for this workflow contract only
// while the candidate leaves CI wiring and its runner implementation intact.
// Code outside these inputs remains eligible for the optimization and is
// validated by the new exact-head CI run after publication.
func verifyWorktreeMergeDirectCIInputs(ctx context.Context, receipt WorktreeMergeReceipt) error {
	if receipt.TargetSHA == "" || receipt.Candidate.SHA == "" || receipt.Candidate.Worktree == "" {
		return fmt.Errorf("direct CI input comparison requires exact target and candidate identities")
	}
	changed, _, err := runCommand(ctx, defaultRunner, 0, 0, receipt.Candidate.Worktree, "git", "diff", "--name-only", receipt.TargetSHA, receipt.Candidate.SHA, "--",
		".github", ".wb", "cmd/wb", "internal/quality", "go.mod", "go.sum")
	if err != nil {
		return fmt.Errorf("compare candidate CI inputs with previously validated target: %w", err)
	}
	if strings.TrimSpace(changed) != "" {
		return fmt.Errorf("direct CI deferral refuses changed CI inputs: %s", strings.Join(strings.Fields(changed), ", "))
	}
	return nil
}

func hasRequiredRemoteCheck(checks []RequiredRemoteCheck, name string) bool {
	for _, check := range checks {
		if check.Name == name {
			return true
		}
	}
	return false
}

func verifyWorktreeMergeDirectCIPullRequest(ctx context.Context, receipt WorktreeMergeReceipt, contract worktreeMergeDirectCIContract, head string) error {
	view, err := ReadPullRequest(ctx, receipt.Repository, contract.PullRequest)
	if err != nil {
		return err
	}
	if !strings.EqualFold(view.State, "open") || view.Merged || view.Head.Ref != receipt.Target || view.Head.Repo == nil || view.Head.Repo.FullName != receipt.Repository ||
		view.Base.Ref != contract.Base || view.Base.Repo == nil || view.Base.Repo.FullName != receipt.Repository || view.Head.SHA != head {
		return fmt.Errorf("direct CI pull request %s no longer points from %s@%s to %s", contract.PullRequest, receipt.Target, head, contract.Base)
	}
	return nil
}
