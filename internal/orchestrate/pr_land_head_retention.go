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
// branch, or "" when it may be retired. It runs on every route to the
// landing tail — a fresh merge, a merge queue, and an already-merged resume.
// A promotion's head, a branch whose commits carry WB lane or refresh merges,
// and a branch any pull request ever targeted are long-lived: retiring one
// deletes the base other work lands on (datatug/datatug-apps#504 deleted
// public-demo) and closes every open pull request still targeting it. It
// fails closed: a read that cannot answer keeps the branch, because keeping
// a branch costs nothing and a later sweep can still retire it.
func integrationHeadReason(ctx context.Context, options PullRequestLandOptions, view githubchecks.PullRequestView, number string, promotion *PromotionReceipt) string {
	if view.Head.Repo == nil || !strings.EqualFold(view.Head.Repo.FullName, options.Repository) || strings.TrimSpace(view.Head.Ref) == "" || view.Head.Ref == view.Base.Ref {
		return "" // nothing of this repository would be retired
	}
	if promotion != nil || strings.Contains(view.Body, promoteMarker) {
		return "it is the integration branch this promotion carries"
	}
	onto := options.pullRequestsOnto
	if onto == nil {
		onto = pullRequestsOnto
	}
	targeted, err := onto(ctx, options.Repository, view.Head.Ref)
	switch {
	case err != nil:
		return "kept because pull requests onto it could not be listed: " + err.Error()
	case targeted > 0:
		return "pull requests have targeted it, so other work lands on it"
	}
	// The one-row read above is cheaper than paging the commits, so it
	// answers first.
	commits := options.sourceCommits
	if commits == nil {
		read, err := pullRequestCommits(ctx, options.Repository, number)
		if err != nil {
			return "kept because its commits could not be read to rule out WB merges: " + err.Error()
		}
		commits = read
	}
	for _, commit := range commits {
		for _, match := range trailerLinePattern.FindAllStringSubmatch(commit.Body, -1) {
			if match[1] == trailerLane || match[1] == trailerRefresh {
				return fmt.Sprintf("commit %s is a WB %s merge, so it is an integration branch", shortMergeRevision(commit.SHA), strings.ToLower(strings.TrimPrefix(match[1], "Wb-")))
			}
		}
	}
	return ""
}

// pullRequestsOnto counts pull requests in any state whose base is branch;
// one is enough to keep it.
func pullRequestsOnto(ctx context.Context, repository, branch string) (int, error) {
	raw, err := githubGet(ctx, "", repository, "", "", "repos/"+repository+"/pulls?state=all&per_page=1&base="+url.QueryEscape(branch))
	if err != nil {
		return 0, err
	}
	var pulls []json.RawMessage
	if err := json.Unmarshal(raw, &pulls); err != nil {
		return 0, fmt.Errorf("decode pull requests onto %s: %w", branch, err)
	}
	return len(pulls), nil
}
