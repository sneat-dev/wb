package landingcontext

import (
	"context"
	"strings"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func SuggestCloses(ctx context.Context, projectsRoot, worktreeArg string) []int {
	return suggestCloses(ctx, projectsRoot, worktreeArg, orchestrate.ResolvePullRequestCreateWorktree, worktrees.LoadWorkLogView)
}
func suggestCloses(ctx context.Context, projectsRoot, worktreeArg string, resolve func(context.Context, string, string) (string, error), load func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error)) []int {
	worktree, err := resolve(ctx, projectsRoot, worktreeArgOrCurrent(worktreeArg))
	if err != nil {
		return nil
	}
	view, err := load(ctx, worktrees.LoadWorkLogOptions{
		ProjectsRoot: projectsRoot, Worktree: worktree, IncludePromptBodies: true,
	})
	if err != nil || view.OriginalPrompt == nil {
		return nil
	}
	return orchestrate.SuggestClosesFromPrompt(view.OriginalPrompt.Body)
}
func worktreeArgOrCurrent(worktreeArg string) string {
	if strings.TrimSpace(worktreeArg) == "" {
		return "."
	}
	return worktreeArg
}
