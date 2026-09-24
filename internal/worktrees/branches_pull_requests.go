package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

// BranchPullRequest describes a PR's relationship to the named branch. This
// is branch history, separate from the immutable merged landing proof.
type BranchPullRequest struct {
	Number   int        `json:"number"`
	URL      string     `json:"url"`
	Role     string     `json:"role"`  // head or base
	State    string     `json:"state"` // open, merged, or closed
	Head     string     `json:"head"`
	Base     string     `json:"base"`
	HeadSHA  string     `json:"head_sha"`
	MergeSHA string     `json:"merge_sha,omitempty"`
	MergedAt *time.Time `json:"merged_at,omitempty"`
}

type branchPullRequestEvidence struct {
	requests []BranchPullRequest
	openHead *PullRequest
	openBase *PullRequest
	err      error
}

func decorateRemoteBranchPullRequests(ctx context.Context, repository discover.Repo, ref branchRef, entry *BranchEntry, cache map[string]branchPullRequestEvidence, withHistory bool) {
	evidence, ok := cache[ref.Name]
	if !ok {
		if withHistory {
			evidence = exactBranchPullRequests(ctx, repository.Path, repository.Slug(), ref.Name)
		} else {
			evidence = openBranchPullRequests(ctx, repository.Path, repository.Slug(), ref.Name)
		}
		cache[ref.Name] = evidence
	}
	entry.PullRequests = evidence.requests
	entry.OpenPullRequest = evidence.openHead
	entry.OpenBasePullRequest = evidence.openBase
	if evidence.err != nil {
		entry.PullRequestQueryFailed = true
		entry.PullRequestQueryError = evidence.err.Error()
	} else {
		entry.PullRequestQueried = true
	}
}

// exactBranchPullRequests asks GitHub for both roles by branch name. Filtering
// the returned identities matters: a commit can be shared by several branches,
// and GitHub's head selector alone does not prove same-repository ownership.
func exactBranchPullRequests(ctx context.Context, worktree, repository, branch string) branchPullRequestEvidence {
	return branchPullRequestsForHeadState(ctx, worktree, repository, branch, "all")
}

func openBranchPullRequests(ctx context.Context, worktree, repository, branch string) branchPullRequestEvidence {
	return branchPullRequestsForHeadState(ctx, worktree, repository, branch, "open")
}

func branchPullRequestsForHeadState(ctx context.Context, worktree, repository, branch, headState string) branchPullRequestEvidence {
	owner, _, ok := strings.Cut(repository, "/")
	if !ok || owner == "" || branch == "" {
		return branchPullRequestEvidence{err: fmt.Errorf("invalid repository or branch for pull-request query")}
	}
	head, err := queryBranchPullRequests(ctx, worktree, repository, url.Values{"head": {owner + ":" + branch}, "state": {headState}, "per_page": {"100"}})
	if err != nil {
		return branchPullRequestEvidence{err: fmt.Errorf("query head pull requests for %s:%s: %w", repository, branch, err)}
	}
	base, err := queryBranchPullRequests(ctx, worktree, repository, url.Values{"base": {branch}, "state": {"open"}, "per_page": {"100"}})
	if err != nil {
		return branchPullRequestEvidence{err: fmt.Errorf("query base pull requests for %s:%s: %w", repository, branch, err)}
	}
	var result branchPullRequestEvidence
	appendCandidate := func(candidate githubPullRequest, role string) {
		state := strings.ToLower(candidate.State)
		if candidate.MergedAt != nil {
			state = "merged"
		}
		if state != "open" && state != "merged" && state != "closed" {
			return
		}
		request := BranchPullRequest{
			Number: candidate.Number, URL: candidate.URL, Role: role, State: state,
			Head: candidate.Head.Ref, Base: candidate.Base.Ref, HeadSHA: candidate.Head.SHA,
			MergeSHA: candidate.MergeCommitSHA, MergedAt: candidate.MergedAt,
		}
		result.requests = append(result.requests, request)
		if state != "open" {
			return
		}
		open := &PullRequest{
			Number: candidate.Number, URL: candidate.URL, Repository: repository,
			State: "OPEN", Base: candidate.Base.Ref, BaseSHA: candidate.Base.SHA,
			HeadSHA: candidate.Head.SHA,
		}
		if role == "head" && (result.openHead == nil || open.Number > result.openHead.Number) {
			result.openHead = open
		}
		if role == "base" && (result.openBase == nil || open.Number > result.openBase.Number) {
			result.openBase = open
		}
	}
	for _, candidate := range head {
		if candidate.Head.Ref == branch && candidate.Head.Repo == nil {
			return branchPullRequestEvidence{err: fmt.Errorf("head pull request #%d for %s:%s has no repository identity", candidate.Number, repository, branch)}
		}
		if candidate.Head.Ref == branch && candidate.Head.Repo != nil && strings.EqualFold(candidate.Head.Repo.FullName, repository) {
			appendCandidate(candidate, "head")
		}
	}
	for _, candidate := range base {
		if candidate.Base.Ref == branch && candidate.Base.Repo == nil {
			return branchPullRequestEvidence{err: fmt.Errorf("base pull request #%d for %s:%s has no repository identity", candidate.Number, repository, branch)}
		}
		if candidate.Base.Ref == branch && candidate.Base.Repo != nil && strings.EqualFold(candidate.Base.Repo.FullName, repository) {
			appendCandidate(candidate, "base")
		}
	}
	sort.Slice(result.requests, func(i, j int) bool {
		if result.requests[i].Role != result.requests[j].Role {
			return result.requests[i].Role < result.requests[j].Role
		}
		return result.requests[i].Number < result.requests[j].Number
	})
	return result
}

func queryBranchPullRequests(ctx context.Context, worktree, repository string, query url.Values) ([]githubPullRequest, error) {
	endpoint := "repos/" + repository + "/pulls?" + query.Encode()
	response := githubobserver.Execute(ctx, worktree, "api", "--paginate", endpoint)
	if response.Err != nil {
		return nil, fmt.Errorf("%w: %s", response.Err, strings.TrimSpace(string(response.Stderr)+string(response.Stdout)))
	}
	var requests []githubPullRequest
	decoder := json.NewDecoder(bytes.NewReader(response.Stdout))
	pageCount := 0
	for {
		var page []githubPullRequest
		if err := decoder.Decode(&page); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode %s: %w", endpoint, err)
		}
		if page == nil {
			return nil, fmt.Errorf("decode %s: expected a pull-request array, got null", endpoint)
		}
		pageCount++
		requests = append(requests, page...)
	}
	if pageCount == 0 {
		return nil, fmt.Errorf("decode %s: expected a pull-request array, got empty response", endpoint)
	}
	return requests, nil
}
