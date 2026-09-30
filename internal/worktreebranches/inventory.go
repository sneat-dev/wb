package worktreebranches

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

	"github.com/sneat-dev/wb/internal/worktreelanding"
	"github.com/sneat-dev/wb/internal/worktreeproof"
)

// Repository is the branch inventory's view of a discovered canonical clone.
// Discovery and admission remain with the facade.
type Repository struct {
	Slug string
	Path string
}

type BranchUse struct {
	Repository string
	Branch     string
	Task       string
}

// GitHubPullRequest is only the GitHub response shape consumed by inventory.
// It is not trusted as a landing receipt until the exact Git proof succeeds.
type GitHubPullRequest struct {
	Number         int        `json:"number"`
	URL            string     `json:"html_url"`
	State          string     `json:"state"`
	Base           GitHubRef  `json:"base"`
	Head           GitHubRef  `json:"head"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	MergedAt       *time.Time `json:"merged_at"`
}

type GitHubRef struct {
	Ref  string            `json:"ref"`
	SHA  string            `json:"sha"`
	Repo *GitHubRepository `json:"repo"`
}

type GitHubRepository struct {
	FullName string `json:"full_name"`
}

type SupersessionEvidence struct {
	Reviewer  string
	ReceiptID string
}

// InventoryPorts supplies host, repository, Git and hosted-PR observations.
// An inventory service has no package-global callbacks or hidden Git runner.
type InventoryPorts struct {
	Discover            func(string) ([]Repository, error)
	ListInUse           func(context.Context, string, string) ([]BranchUse, error)
	Git                 worktreeproof.GitQuery
	FetchTarget         func(context.Context, string, string) (string, error)
	IsAncestor          func(context.Context, string, string, string) (bool, error)
	ContentContained    func(context.Context, string, string, string) (bool, error)
	CommitTree          func(context.Context, string, string) (string, error)
	Supersession        func(context.Context, string, Repository, BranchRef, string, string) (*SupersessionEvidence, string)
	SupersessionDigest  func(string) (string, error)
	AttestedReceipt     func(context.Context, Repository, BranchRef, string, string, string) (*worktreelanding.VerifiedCandidate, string, error)
	PullRequestsForHead func(context.Context, Repository, string) ([]GitHubPullRequest, error)
	AbsorbingPR         func([]GitHubPullRequest, string) *PullRequest
	PullRequestAPI      func(context.Context, string, string) ([]byte, error)
	HostName            func() (string, error)
	TempDir             func() (string, error)
	RemoveDir           func(string) error
}

type InventoryService struct{ Ports InventoryPorts }

type InventorySweep struct {
	ProjectsRoot, Base, Scope, Only, Filter, Repository, Org, Branch, Name string
	OlderThan                                                              time.Duration
	Progress                                                               io.Writer
	Now                                                                    time.Time
	Receipts, WithPRs, Cleanup, IncludeRetired                             bool
	AbsorbedBy, SupersededBy                                               string
}

func (s InventorySweep) Policy() PolicyOptions {
	return PolicyOptions{Base: s.Base, Scope: s.Scope, Only: s.Only, Branch: s.Branch, Name: s.Name, OlderThan: s.OlderThan, Now: s.Now}
}

type PullRequestEvidence struct {
	Requests []BranchPullRequest
	OpenHead *PullRequest
	OpenBase *PullRequest
	Err      error
}

// DecorateRemoteBranchPullRequests caches by exact branch name so one sweep
// never asks GitHub twice for the same head/base evidence.
func (service InventoryService) DecorateRemoteBranchPullRequests(ctx context.Context, repository Repository, ref BranchRef, entry *BranchEntry, cache map[string]PullRequestEvidence, withHistory bool) {
	evidence, ok := cache[ref.Name]
	if !ok {
		if withHistory {
			evidence = service.ExactBranchPullRequests(ctx, repository, ref.Name)
		} else {
			evidence = service.OpenBranchPullRequests(ctx, repository, ref.Name)
		}
		cache[ref.Name] = evidence
	}
	entry.PullRequests = evidence.Requests
	entry.OpenPullRequest = evidence.OpenHead
	entry.OpenBasePullRequest = evidence.OpenBase
	if evidence.Err != nil {
		entry.PullRequestQueryFailed = true
		entry.PullRequestQueryError = evidence.Err.Error()
	} else {
		entry.PullRequestQueried = true
	}
}

func (service InventoryService) ExactBranchPullRequests(ctx context.Context, repository Repository, branch string) PullRequestEvidence {
	return service.BranchPullRequestsForHeadState(ctx, repository, branch, "all")
}

func (service InventoryService) OpenBranchPullRequests(ctx context.Context, repository Repository, branch string) PullRequestEvidence {
	return service.BranchPullRequestsForHeadState(ctx, repository, branch, "open")
}

func (service InventoryService) BranchPullRequestsForHeadState(ctx context.Context, repository Repository, branch, headState string) PullRequestEvidence {
	owner, _, ok := strings.Cut(repository.Slug, "/")
	if !ok || owner == "" || branch == "" {
		return PullRequestEvidence{Err: fmt.Errorf("invalid repository or branch for pull-request query")}
	}
	head, err := service.QueryBranchPullRequests(ctx, repository, url.Values{"head": {owner + ":" + branch}, "state": {headState}, "per_page": {"100"}})
	if err != nil {
		return PullRequestEvidence{Err: fmt.Errorf("query head pull requests for %s:%s: %w", repository.Slug, branch, err)}
	}
	base, err := service.QueryBranchPullRequests(ctx, repository, url.Values{"base": {branch}, "state": {"open"}, "per_page": {"100"}})
	if err != nil {
		return PullRequestEvidence{Err: fmt.Errorf("query base pull requests for %s:%s: %w", repository.Slug, branch, err)}
	}
	var result PullRequestEvidence
	appendCandidate := func(candidate GitHubPullRequest, role string) {
		state := strings.ToLower(candidate.State)
		if candidate.MergedAt != nil {
			state = "merged"
		}
		if state != "open" && state != "merged" && state != "closed" {
			return
		}
		request := BranchPullRequest{Number: candidate.Number, URL: candidate.URL, Role: role, State: state,
			Head: candidate.Head.Ref, Base: candidate.Base.Ref, HeadSHA: candidate.Head.SHA,
			MergeSHA: candidate.MergeCommitSHA, MergedAt: candidate.MergedAt}
		result.Requests = append(result.Requests, request)
		if state != "open" {
			return
		}
		open := &PullRequest{Number: candidate.Number, URL: candidate.URL, Repository: repository.Slug,
			State: "OPEN", Base: candidate.Base.Ref, BaseSHA: candidate.Base.SHA, HeadSHA: candidate.Head.SHA}
		if role == "head" && (result.OpenHead == nil || open.Number > result.OpenHead.Number) {
			result.OpenHead = open
		}
		if role == "base" && (result.OpenBase == nil || open.Number > result.OpenBase.Number) {
			result.OpenBase = open
		}
	}
	for _, candidates := range []struct {
		role      string
		requests  []GitHubPullRequest
		reference func(GitHubPullRequest) GitHubRef
	}{
		{role: "head", requests: head, reference: func(candidate GitHubPullRequest) GitHubRef { return candidate.Head }},
		{role: "base", requests: base, reference: func(candidate GitHubPullRequest) GitHubRef { return candidate.Base }},
	} {
		for _, candidate := range candidates.requests {
			ref := candidates.reference(candidate)
			if ref.Ref == branch && ref.Repo == nil {
				return PullRequestEvidence{Err: fmt.Errorf("%s pull request #%d for %s:%s has no repository identity", candidates.role, candidate.Number, repository.Slug, branch)}
			}
			if ref.Ref == branch && ref.Repo != nil && strings.EqualFold(ref.Repo.FullName, repository.Slug) {
				appendCandidate(candidate, candidates.role)
			}
		}
	}
	sort.Slice(result.Requests, func(i, j int) bool {
		if result.Requests[i].Role != result.Requests[j].Role {
			return result.Requests[i].Role < result.Requests[j].Role
		}
		return result.Requests[i].Number < result.Requests[j].Number
	})
	return result
}

func (service InventoryService) QueryBranchPullRequests(ctx context.Context, repository Repository, query url.Values) ([]GitHubPullRequest, error) {
	endpoint := "repos/" + repository.Slug + "/pulls?" + query.Encode()
	raw, err := service.Ports.PullRequestAPI(ctx, repository.Path, endpoint)
	if err != nil {
		return nil, err
	}
	var requests []GitHubPullRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	pageCount := 0
	for {
		var page []GitHubPullRequest
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
