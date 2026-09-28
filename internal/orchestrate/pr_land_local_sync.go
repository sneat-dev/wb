package orchestrate

import (
	"context"
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/runner"
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
	// Threading options.resolveRunner() through here (rather than an
	// Options{} zero value, which would always fall back to defaultRunner)
	// is what lets a unit test substitute a runnertest.Fake for this call
	// too, so this whole function is reachable end to end without a real
	// process (spec/plans/coverage-to-100 task-17).
	worktree, err := registeredWorktreeForBranch(ctx, canonical, branch, Options{run: options.resolveRunner()})
	if err != nil || strings.TrimSpace(worktree) == "" {
		// No worktree holds the branch, or the branch is (unusually) checked
		// out in the canonical clone itself: neither is this landing's to
		// report.
		return ""
	}
	return fastForwardWorktreeToUpdatedHead(ctx, options.resolveGit(), options.resolveRunner(), worktree, branch, updatedHead)
}

// fastForwardWorktreeToUpdatedHead brings a local WB worktree that has
// branch checked out into fast-forward alignment with updatedHead, a head a
// server-side update-branch (or an equivalent server-side advance) just
// produced. It is the one shared helper both this route and the
// `wb worktree merge` PR route's own update-branch/adopt paths
// (worktree_merge.go's advancePublishedWorktreeMergeCandidate,
// worktree_merge_pr_land.go's adoptWorktreeMergeUpdateBranchAdvance and
// adoptServerUpdatedWorktreeMergeHead) use to fast-forward a local
// candidate worktree onto a server-recorded advance.
//
// It never resets, stashes, rebases, or discards anything. Every obstacle —
// no worktree, a dirty tree, HEAD not on branch, local commits the remote
// does not have, or a fetched head that disagrees with updatedHead — becomes
// a short note in the returned string; only a clean worktree whose HEAD is
// an ancestor of the newly fetched branch is fast-forwarded.
func fastForwardWorktreeToUpdatedHead(ctx context.Context, git Git, run runner.Runner, worktree, branch, updatedHead string) (note string) {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return ""
	}
	prefix := fmt.Sprintf("local worktree not fast-forwarded: %s: ", worktree)

	status, err := git.StatusPorcelain(ctx, worktree)
	if err != nil {
		return prefix + err.Error()
	}
	if strings.TrimSpace(status) != "" {
		return prefix + "uncommitted changes"
	}

	current, err := git.BranchShowCurrent(ctx, worktree)
	if err != nil {
		return prefix + err.Error()
	}
	if strings.TrimSpace(current) != branch {
		return prefix + "HEAD is not on " + branch
	}

	remoteRef := "refs/remotes/origin/" + branch
	refspec := "+refs/heads/" + branch + ":" + remoteRef
	if _, _, err := runCommand(ctx, run, 0, 0, worktree, "git", "fetch", "--no-tags", "origin", refspec); err != nil {
		return prefix + "fetch failed: " + err.Error()
	}

	fetched, err := mergeRevision(ctx, run, worktree, remoteRef)
	if err != nil {
		return prefix + err.Error()
	}
	if fetched != updatedHead {
		return prefix + fmt.Sprintf("fetched head %s does not match updated head %s",
			shortMergeRevision(fetched), shortMergeRevision(updatedHead))
	}

	if ancestorErr := git.MergeBaseIsAncestorStrict(ctx, worktree, "HEAD", remoteRef); ancestorErr != nil {
		return prefix + "diverged local commits"
	}

	if _, _, err := runCommand(ctx, run, 0, 0, worktree, "git", "merge", "--ff-only", remoteRef); err != nil {
		return prefix + "fast-forward failed: " + err.Error()
	}

	// Best effort: the fast-forward already succeeded, and a tracking-branch
	// slip is not worth reporting as an obstacle to it.
	_ = git.BranchSetUpstreamTo(ctx, worktree, "origin/"+branch, branch)

	return fmt.Sprintf("fast-forwarded worktree %s to %s", worktree, shortMergeRevision(updatedHead))
}
