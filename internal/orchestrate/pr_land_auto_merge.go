package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// enablePullRequestAutoMerge asks GitHub to merge this pull request once its
// required checks pass, without WB present.
//
// It exists because a bounded wait has to end somewhere, and the two ways it
// can end are not equal. Returning "checks are still pending" leaves a complete,
// green-pending change stranded on the next person to remember it — which is
// the failure this whole area exists to remove. Arming auto-merge instead means
// the wait ending is not the work stopping.
//
// This is a durable authorization: it outlives the invocation and merges with
// nobody watching. It is therefore armed only where the caller already carried
// authority to merge now — the same approval, the same lane, the same
// server-enforced required checks — and never as a way to land something that
// could not have been landed directly.
func enablePullRequestAutoMerge(ctx context.Context, repository, number, mergeMethod, head, subject, body string) string {
	method := strings.ToUpper(strings.TrimSpace(mergeMethod))
	switch method {
	case "MERGE", "SQUASH", "REBASE":
	default:
		method = "MERGE"
	}
	// The pull request's node id is what the mutation addresses; the REST
	// number is not accepted.
	nodeID, reason := pullRequestNodeID(ctx, repository, number)
	if reason != "" {
		return reason
	}
	// expectedHeadOid pins the arming to the head WB observed, and the commit
	// message is WB's own: when GitHub performs the merge it uses what it was
	// armed with, not the subject and body WB would have sent.
	mutation := "mutation($id:ID!,$method:PullRequestMergeMethod!,$head:GitObjectID!,$subject:String!,$body:String!){" +
		"enablePullRequestAutoMerge(input:{pullRequestId:$id,mergeMethod:$method,expectedHeadOid:$head,commitHeadline:$subject,commitBody:$body}){" +
		"pullRequest{autoMergeRequest{enabledAt}}}}"
	response := githubExecute(ctx, "", "api", "graphql",
		"-f", "query="+mutation,
		"-f", "id="+nodeID,
		"-f", "method="+method,
		"-f", "head="+head,
		"-f", "subject="+subject,
		"-f", "body="+body)
	if response.ExitCode != 0 {
		message := strings.TrimSpace(string(response.Stderr))
		if message == "" {
			message = strings.TrimSpace(string(response.Stdout))
		}
		return fmt.Sprintf("enable auto-merge: %s", message)
	}
	return ""
}

// pullRequestNodeID reads the GraphQL node id for one pull request.
func pullRequestNodeID(ctx context.Context, repository, number string) (string, string) {
	output, err := githubGet(ctx, "", repository, "", "", "repos/"+repository+"/pulls/"+number)
	if err != nil {
		return "", fmt.Sprintf("read pull request node id: %v", err)
	}
	var view struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(output, &view); err != nil {
		return "", fmt.Sprintf("decode pull request node id: %v", err)
	}
	if strings.TrimSpace(view.NodeID) == "" {
		return "", "pull request response carried no node id"
	}
	return view.NodeID, ""
}

// autoMergeBypassesAGuard names the guard arming auto-merge would skip, or
// returns "" when arming is safe.
//
// Auto-merge hands the merge to GitHub, which enforces only what the target's
// branch protection enforces. Two things this verb guarantees are not branch
// protection: --keep-commits merges a branch WB rebuilds first, so GitHub would
// merge the original instead; and on a target without a strict up-to-date
// policy, green checks prove only that the head was green, which is exactly
// what --allow-unfenced exists to make an explicit choice.
func autoMergeBypassesAGuard(ctx context.Context, options PullRequestLandOptions, target string) string {
	if len(options.KeepCommits) > 0 {
		return "--keep-commits merges a rebuilt branch, not this head"
	}
	if options.AllowUnfenced {
		return ""
	}
	_, freshnessAuthority, reason := targetBranchRequiredChecks(ctx, options.Repository, target, true)
	if reason != "" {
		return "target policy unreadable: " + reason
	}
	if freshnessAuthority == "" {
		return target + " has no strict up-to-date policy; pass --allow-unfenced to arm anyway"
	}
	return ""
}
