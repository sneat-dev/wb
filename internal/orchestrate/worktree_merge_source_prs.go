package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

const absorbedSourcePRCommentMarker = "<!-- wb:absorbed-source-pr -->"

type sourcePullRequestRemote interface {
	associated(context.Context, string, string) ([]PullRequestView, error)
	hasComment(context.Context, string, int, string) (bool, error)
	comment(context.Context, string, int, string) error
	close(context.Context, string, int) error
}

type githubSourcePullRequestRemote struct{}

func reconcileAbsorbedSourcePullRequests(ctx context.Context, projectsRoot string, receipt *WorktreeMergeReceipt, timeout time.Duration, retry int) error {
	if receipt == nil || receipt.LandingSHA == "" || receipt.Repository == "" {
		return nil
	}
	heads, err := absorbedSourceHeads(ctx, filepath.Join(projectsRoot, filepath.FromSlash(receipt.Repository)), *receipt, timeout, retry)
	if err != nil {
		return fmt.Errorf("discover absorbed source heads: %w", err)
	}
	return reconcileAbsorbedSourcePullRequestsWith(ctx, receipt, heads, githubSourcePullRequestRemote{}, persistWorktreeMergeReceipt)
}

func absorbedSourceHeads(ctx context.Context, repository string, receipt WorktreeMergeReceipt, timeout time.Duration, retry int) ([]string, error) {
	if receipt.TargetSHA == "" || receipt.Candidate.SHA == "" {
		return nil, fmt.Errorf("landing receipt lacks target or candidate identity")
	}
	heads := make(map[string]bool, len(receipt.Sources))
	for _, source := range receipt.Sources {
		if source.Merged && source.SHA != "" {
			heads[source.SHA] = true
		}
	}
	output, _, err := runCommand(ctx, timeout, retry, repository, "git", "rev-list", "--merges", "--parents", receipt.TargetSHA+".."+receipt.Candidate.SHA)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		for _, parent := range fields[2:] {
			heads[parent] = true
		}
	}
	delete(heads, receipt.TargetSHA)
	delete(heads, receipt.Candidate.SHA)
	result := make([]string, 0, len(heads))
	for head := range heads {
		result = append(result, head)
	}
	sort.Strings(result)
	return result, nil
}

func reconcileAbsorbedSourcePullRequestsWith(
	ctx context.Context,
	receipt *WorktreeMergeReceipt,
	heads []string,
	remote sourcePullRequestRemote,
	persist func(WorktreeMergeReceipt) error,
) error {
	integrationNumber, _ := PullRequestNumber(receipt.PullRequest)
	seen := make(map[string]bool)
	for _, head := range heads {
		views, err := remote.associated(ctx, receipt.Repository, head)
		if err != nil {
			return fmt.Errorf("discover pull requests for absorbed source %s: %w", head, err)
		}
		for _, view := range views {
			key := fmt.Sprintf("%d/%s", view.Number, head)
			if seen[key] || integrationNumber == fmt.Sprint(view.Number) {
				continue
			}
			seen[key] = true
			reconciliation := findSourcePullRequestReconciliation(receipt, view.Number, head)
			reconciliation.Number = view.Number
			reconciliation.URL = view.HTMLURL
			reconciliation.SourceSHA = head
			reconciliation.ObservedSHA = view.Head.SHA
			reconciliation.ObservedBase = view.Base.Ref
			reconciliation.UpdatedAt = time.Now().UTC()
			if view.Head.SHA != head {
				reconciliation.Outcome = "head_advanced"
				reconciliation.Reason = fmt.Sprintf("pull request head advanced to %s", view.Head.SHA)
				if err := persist(*receipt); err != nil {
					return err
				}
				continue
			}
			if view.Base.Ref != receipt.Target || view.Base.Repo == nil || view.Base.Repo.FullName != receipt.Repository {
				reconciliation.Outcome = "base_mismatch"
				reconciliation.Reason = fmt.Sprintf("pull request targets %s, not %s", view.Base.Ref, receipt.Target)
				if err := persist(*receipt); err != nil {
					return err
				}
				continue
			}
			if reconciliation.Closed && reconciliation.Commented {
				continue
			}
			if !reconciliation.Commented {
				commented, err := remote.hasComment(ctx, receipt.Repository, view.Number, absorbedSourcePRCommentMarker)
				if err != nil {
					return fmt.Errorf("inspect absorbed source pull request comments %s: %w", view.HTMLURL, err)
				}
				if !commented {
					absorber := "the WB batch"
					if receipt.PullRequest != "" {
						absorber = receipt.PullRequest
					}
					body := absorbedSourcePRCommentMarker + "\nWB verified that exact head `" + head + "` was absorbed by " + absorber +
						" and that landing `" + receipt.LandingSHA + "` is on `" + receipt.Target + "`. No additional merge is needed."
					if err := remote.comment(ctx, receipt.Repository, view.Number, body); err != nil {
						return fmt.Errorf("comment on absorbed source pull request %s: %w", view.HTMLURL, err)
					}
				}
				reconciliation.Commented = true
				reconciliation.UpdatedAt = time.Now().UTC()
				if err := persist(*receipt); err != nil {
					return err
				}
			}
			if strings.EqualFold(view.State, "open") && !reconciliation.Closed {
				if err := remote.close(ctx, receipt.Repository, view.Number); err != nil {
					return fmt.Errorf("close absorbed source pull request %s: %w", view.HTMLURL, err)
				}
				reconciliation.Closed = true
				reconciliation.Outcome = "closed_absorbed"
				reconciliation.Reason = "exact source head was absorbed by the verified batch landing"
				reconciliation.UpdatedAt = time.Now().UTC()
				if err := persist(*receipt); err != nil {
					return err
				}
			} else if !strings.EqualFold(view.State, "open") {
				reconciliation.Closed = true
				reconciliation.Outcome = "already_closed"
				reconciliation.Reason = "exact source pull request was already closed when the batch landing was reconciled"
				reconciliation.UpdatedAt = time.Now().UTC()
				if err := persist(*receipt); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func findSourcePullRequestReconciliation(receipt *WorktreeMergeReceipt, number int, sourceSHA string) *WorktreeMergeSourcePullRequestReconciliation {
	for index := range receipt.SourcePullRequests {
		item := &receipt.SourcePullRequests[index]
		if item.Number == number && item.SourceSHA == sourceSHA {
			return item
		}
	}
	receipt.SourcePullRequests = append(receipt.SourcePullRequests, WorktreeMergeSourcePullRequestReconciliation{})
	return &receipt.SourcePullRequests[len(receipt.SourcePullRequests)-1]
}

func (githubSourcePullRequestRemote) associated(ctx context.Context, repository, head string) ([]PullRequestView, error) {
	body, err := githubGet(ctx, "", repository, "", head, "repos/"+repository+"/commits/"+url.PathEscape(head)+"/pulls?per_page=100")
	if err != nil {
		return nil, err
	}
	var views []PullRequestView
	if err := json.Unmarshal(body, &views); err != nil {
		return nil, err
	}
	return views, nil
}

func (githubSourcePullRequestRemote) hasComment(ctx context.Context, repository string, number int, marker string) (bool, error) {
	responses, err := githubobserver.GetPages(ctx, githubobserver.GetRequest{
		Repository: repository,
		Endpoint:   fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", repository, number),
	}, 0)
	if err != nil {
		return false, err
	}
	for _, response := range responses {
		var comments []struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(response.Body, &comments); err != nil {
			return false, err
		}
		for _, comment := range comments {
			if strings.Contains(comment.Body, marker) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (githubSourcePullRequestRemote) comment(ctx context.Context, repository string, number int, body string) error {
	result := githubExecute(ctx, "", "api", "--method", "POST", fmt.Sprintf("repos/%s/issues/%d/comments", repository, number), "-f", "body="+body)
	return githubMutationError(result, "post comment")
}

func (githubSourcePullRequestRemote) close(ctx context.Context, repository string, number int) error {
	result := githubExecute(ctx, "", "api", "--method", "PATCH", fmt.Sprintf("repos/%s/pulls/%d", repository, number), "-f", "state=closed")
	return githubMutationError(result, "close pull request")
}

func githubMutationError(result githubobserver.CommandResponse, action string) error {
	if result.Err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(result.Stderr) + "\n" + string(result.Stdout))
	if detail == "" {
		return fmt.Errorf("%s: %w", action, result.Err)
	}
	return fmt.Errorf("%s: %w: %s", action, result.Err, detail)
}
