package orchestrate

import (
	"context"
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// syncLocalWorktreeAfterUpdateBranch is `wb pr land`'s call site for #611:
// once a server-side update-branch has succeeded and the updated head has
// been re-read, bring the local WB worktree that holds the PR's head branch
// (if any) up to that head, so a follow-up commit and `git push` from that
// worktree are not rejected as non-fast-forward.
//
// It is best effort end to end: any failure to find, or to fast-forward, the
// worktree becomes a note in the returned string, and the landing continues
// regardless. It never blocks or fails the landing.
func syncLocalWorktreeAfterUpdateBranch(ctx context.Context, options PullRequestLandOptions, branch, updatedHead string) (note string) {
	canonical, err := worktrees.CanonicalRepositoryPath(options.ProjectsRoot, options.Repository)
	if err != nil {
		return ""
	}
	// registeredWorktreeForBranch (engine.go) is the same `git worktree list
	// --porcelain` lookup every resumed operation already uses to find the
	// linked worktree for a branch; it lists every worktree of the
	// repository's canonical clone, not only ones WB itself registered.
	worktree, err := registeredWorktreeForBranch(ctx, canonical, branch, Options{})
	if err != nil || strings.TrimSpace(worktree) == "" {
		// No worktree holds the branch, or the branch is (unusually) checked
		// out in the canonical clone itself: neither is this landing's to
		// report.
		return ""
	}
	return fastForwardWorktreeToUpdatedHead(ctx, worktree, branch, updatedHead)
}

// fastForwardWorktreeToUpdatedHead brings a local WB worktree that has
// branch checked out into fast-forward alignment with updatedHead, a head a
// server-side update-branch (or an equivalent server-side advance) just
// produced. It mirrors the shape worktree_merge_pr_land.go's
// fastForwardWorktreeMergeCandidateBranch uses for the `wb worktree merge`
// PR route, so the two can later be folded into one shared helper.
//
// It never resets, stashes, rebases, or discards anything. Every obstacle —
// no worktree, a dirty tree, HEAD not on branch, local commits the remote
// does not have, or a fetched head that disagrees with updatedHead — becomes
// a short note in the returned string; only a clean worktree whose HEAD is
// an ancestor of the newly fetched branch is fast-forwarded.
func fastForwardWorktreeToUpdatedHead(ctx context.Context, worktree, branch, updatedHead string) (note string) {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return ""
	}
	prefix := fmt.Sprintf("local worktree not fast-forwarded: %s: ", worktree)

	status, err := runGit(ctx, worktree, "status", "--porcelain")
	if err != nil {
		return prefix + err.Error()
	}
	if strings.TrimSpace(status) != "" {
		return prefix + "uncommitted changes"
	}

	current, err := runGit(ctx, worktree, "branch", "--show-current")
	if err != nil {
		return prefix + err.Error()
	}
	if strings.TrimSpace(current) != branch {
		return prefix + "HEAD is not on " + branch
	}

	remoteRef := "refs/remotes/origin/" + branch
	refspec := "+refs/heads/" + branch + ":" + remoteRef
	if _, _, err := runCommand(ctx, 0, 0, worktree, "git", "fetch", "--no-tags", "origin", refspec); err != nil {
		return prefix + "fetch failed: " + err.Error()
	}

	fetched, err := mergeRevision(ctx, worktree, remoteRef)
	if err != nil {
		return prefix + err.Error()
	}
	if fetched != updatedHead {
		return prefix + fmt.Sprintf("fetched head %s does not match updated head %s",
			shortMergeRevision(fetched), shortMergeRevision(updatedHead))
	}

	if _, ancestorErr := runGit(ctx, worktree, "merge-base", "--is-ancestor", "HEAD", remoteRef); ancestorErr != nil {
		return prefix + "diverged local commits"
	}

	if _, _, err := runCommand(ctx, 0, 0, worktree, "git", "merge", "--ff-only", remoteRef); err != nil {
		return prefix + "fast-forward failed: " + err.Error()
	}

	// Best effort: the fast-forward already succeeded, and a tracking-branch
	// slip is not worth reporting as an obstacle to it.
	_, _ = runGit(ctx, worktree, "branch", "--set-upstream-to=origin/"+branch, branch)

	return fmt.Sprintf("fast-forwarded worktree %s to %s", worktree, shortMergeRevision(updatedHead))
}
