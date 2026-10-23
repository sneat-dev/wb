package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

// integrationHeadReason says why landing must keep the pull request's head
// branch, or "" when it may be retired. A promotion's head, a branch whose
// commits carry WB lane or refresh merges, and a branch other open pull
// requests target are long-lived integration branches: retiring one deletes
// the base other work lands on (datatug/datatug-apps#504 deleted
// public-demo). A failed open-pull-request read is recorded and does not
// retain the branch.
func integrationHeadReason(ctx context.Context, options PullRequestLandOptions, view githubchecks.PullRequestView, commits []SourceCommit, promotion *PromotionReceipt, evidence map[string]string) string {
	if promotion != nil {
		return "it is the integration branch this promotion carries"
	}
	for _, commit := range commits {
		for _, match := range trailerLinePattern.FindAllStringSubmatch(commit.Body, -1) {
			if match[1] == trailerLane || match[1] == trailerRefresh {
				return fmt.Sprintf("commit %s is a WB %s merge, so it is an integration branch", shortMergeRevision(commit.SHA), strings.ToLower(strings.TrimPrefix(match[1], "Wb-")))
			}
		}
	}
	onto := options.openPullRequestsOnto
	if onto == nil {
		onto = openPullRequestsOnto
	}
	open, err := onto(ctx, options.Repository, view.Head.Ref)
	if err != nil {
		evidence["head_branch_use"] = "could not list open pull requests onto " + view.Head.Ref + ": " + err.Error()
		return ""
	}
	if open > 0 {
		return fmt.Sprintf("%d open pull request(s) target it", open)
	}
	return ""
}

// openPullRequestsOnto counts open pull requests whose base is branch (one
// page is enough: any is a reason to keep it).
func openPullRequestsOnto(ctx context.Context, repository, branch string) (int, error) {
	raw, err := githubGet(ctx, "", repository, "", "", "repos/"+repository+"/pulls?state=open&per_page=10&base="+url.QueryEscape(branch))
	if err != nil {
		return 0, err
	}
	var pulls []json.RawMessage
	if err := json.Unmarshal(raw, &pulls); err != nil {
		return 0, fmt.Errorf("decode open pull requests onto %s: %w", branch, err)
	}
	return len(pulls), nil
}
