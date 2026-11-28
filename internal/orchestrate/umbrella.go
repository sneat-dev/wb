package orchestrate

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/umbrella"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func resolveUmbrellaProviders(custom *umbrella.Registry) *umbrella.Registry {
	if custom != nil {
		return custom
	}
	return umbrella.DefaultProviders(nil)
}

func pullRequestHeadRepository(view githubchecks.PullRequestView) string {
	if view.Head.Repo != nil {
		return strings.TrimSpace(view.Head.Repo.FullName)
	}
	return ""
}

func commitMessageWithUmbrella(projectsRoot, worktree, message string) string {
	raw := worktrees.UmbrellaForWorktree(projectsRoot, worktree)
	if raw == "" {
		return message
	}
	ref, err := umbrella.Parse(raw, "")
	if err != nil {
		return message
	}
	return umbrella.AppendTrailer(message, ref)
}

func commitMessageWithTaskUmbrella(projectsRoot, task, message string) string {
	raw := worktrees.UmbrellaForTask(projectsRoot, task)
	if raw == "" {
		return message
	}
	ref, err := umbrella.Parse(raw, "")
	if err != nil {
		return message
	}
	return umbrella.AppendTrailer(message, ref)
}

func worktreeMergeIntegrationArgs(projectsRoot string, sources []WorktreeMergeSource, subject, sha string) []string {
	seen := map[string]umbrella.Ref{}
	for _, source := range sources {
		raw := ""
		if source.Worktree != "" {
			raw = worktrees.UmbrellaForWorktree(projectsRoot, source.Worktree)
		}
		if raw == "" && source.Task != "" {
			raw = worktrees.UmbrellaForTask(projectsRoot, source.Task)
		}
		if raw == "" {
			continue
		}
		ref, err := umbrella.Parse(raw, "")
		if err != nil {
			continue
		}
		seen[ref.String()] = ref
	}
	if len(seen) == 0 {
		return []string{"merge", "--no-edit", sha}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	msg := subject
	for _, k := range keys {
		msg = umbrella.AppendTrailer(msg, seen[k])
	}
	return []string{"merge", "--no-edit", "-m", msg, sha}
}

func pullRequestBodyWithUmbrella(projectsRoot, worktree, body string, providers *umbrella.Registry) (string, string, error) {
	raw := worktrees.UmbrellaForWorktree(projectsRoot, worktree)
	if raw == "" {
		return body, "", nil
	}
	ref, err := umbrella.Parse(raw, "")
	if err != nil {
		return body, "", err
	}
	link, err := resolveUmbrellaProviders(providers).RenderLink(ref)
	if err != nil {
		return body, "", err
	}
	return umbrella.WithPartOf(body, link, ref), ref.String(), nil
}

func reportPullRequestLandedUmbrella(ctx context.Context, options PullRequestLandOptions, result *PullRequestLandResult, headRepo, number string) {
	if result == nil {
		return
	}
	worktrees.RecordPullRequestLandedSHA(options.ProjectsRoot, options.Repository, result.PullRequest, headRepo, result.HeadRef, result.MergeSHA)
	ref, task, ok := worktrees.UmbrellaForPullRequest(options.ProjectsRoot, options.Repository, result.PullRequest, headRepo, result.HeadRef, result.CleanedTasks)
	if !ok {
		return
	}
	result.Umbrella = ref.String()
	line := umbrella.FormatStatusLine(umbrella.StatusUpdate{
		Task:        task,
		Repository:  options.Repository,
		PullRequest: "#" + number,
		Outcome:     "landed",
		LandedSHA:   result.MergeSHA,
	})
	if err := resolveUmbrellaProviders(options.UmbrellaProviders).AppendStatus(ctx, ref, line); err != nil {
		result.Evidence["umbrella_status_error"] = err.Error()
	} else {
		result.Evidence["umbrella_status"] = "posted"
	}
	if ready, _, err := worktrees.UmbrellaReadyToClose(options.ProjectsRoot, ref); err == nil && ready {
		result.UmbrellaCloseCommand = umbrella.CloseCommand(ref)
	}
}

func recordWorktreeMergeLandedClaims(projectsRoot string, receipt WorktreeMergeReceipt) {
	if receipt.LandingSHA == "" {
		return
	}
	prNum := 0
	if receipt.PullRequest != "" {
		if numStr, err := githubchecks.PullRequestNumber(receipt.PullRequest); err == nil && numStr != "" {
			if parsed, convErr := strconv.Atoi(numStr); convErr == nil && parsed > 0 {
				prNum = parsed
			}
		}
	}
	for _, source := range receipt.Sources {
		_ = worktrees.RecordClaimLanding(
			projectsRoot,
			receipt.Repository,
			source.Task,
			source.Worktree,
			source.Branch,
			receipt.Target,
			receipt.LandingSHA,
			prNum,
			receipt.PullRequest,
		)
	}
}

func reportWorktreeMergeLandedUmbrella(ctx context.Context, projectsRoot string, providers *umbrella.Registry, receipt *WorktreeMergeReceipt) {
	if receipt == nil || receipt.LandingSHA == "" {
		return
	}
	recordWorktreeMergeLandedClaims(projectsRoot, *receipt)
	reg := resolveUmbrellaProviders(providers)
	prLabel := ""
	if receipt.PullRequest != "" {
		if num, err := githubchecks.PullRequestNumber(receipt.PullRequest); err == nil && num != "" {
			prLabel = "#" + num
		}
	}
	type postedKey struct{ ref, task string }
	seen := map[postedKey]bool{}
	for _, source := range receipt.Sources {
		raw := worktrees.UmbrellaForTask(projectsRoot, source.Task)
		if raw == "" && source.Worktree != "" {
			raw = worktrees.UmbrellaForWorktree(projectsRoot, source.Worktree)
		}
		if raw == "" {
			continue
		}
		ref, err := umbrella.Parse(raw, "")
		if err != nil {
			continue
		}
		key := postedKey{ref: ref.String(), task: source.Task}
		if seen[key] {
			continue
		}
		seen[key] = true
		if receipt.Umbrella == "" {
			receipt.Umbrella = ref.String()
		}
		line := umbrella.FormatStatusLine(umbrella.StatusUpdate{
			Task:        source.Task,
			Repository:  receipt.Repository,
			PullRequest: prLabel,
			Outcome:     "landed",
			LandedSHA:   receipt.LandingSHA,
		})
		if err := reg.AppendStatus(ctx, ref, line); err != nil {
			receipt.Findings = append(receipt.Findings, WorktreeMergeFinding{
				Code:    "umbrella-status-error",
				Message: fmt.Sprintf("failed to post landed status to umbrella %s: %v", ref.String(), err),
			})
		}
		if ready, _, readyErr := worktrees.UmbrellaReadyToClose(projectsRoot, ref); readyErr == nil && ready && receipt.UmbrellaCloseCommand == "" {
			receipt.UmbrellaCloseCommand = umbrella.CloseCommand(ref)
		}
	}
}

func appendPullRequestUmbrellaTrailer(projectsRoot, repository string, pullRequest int, headRepo, headRef, body string) string {
	ref, _, ok := worktrees.UmbrellaForPullRequest(projectsRoot, repository, pullRequest, headRepo, headRef, nil)
	if !ok {
		return body
	}
	return strings.TrimRight(umbrella.AppendTrailer(body, ref), "\n") + "\n"
}
