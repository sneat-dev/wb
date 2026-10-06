package worktreerun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktreeend"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
	"os/exec"
	"strings"
	"time"
)

func NewEndEngine(projectsRoot string, claimOut io.Writer, release func(string, string, io.Writer)) (*worktreeend.Engine, error) {
	store, err := streams.Open(projectsRoot)
	if err != nil {
		return nil, err
	}
	return &worktreeend.Engine{ProjectsRoot: projectsRoot, Inventory: worktreeInventory{}, Links: streamLinkGuard{store: store}, Capture: gitStashCapture{}, Notes: workLogNotes{}, Retirer: cleanupRetirer{}, Claims: claimReleaser{writer: claimOut, release: release}}, nil
}

// worktreeInventory lists a task's checkouts through the existing inventory.
type worktreeInventory struct{}

func (worktreeInventory) Worktrees(ctx context.Context, projectsRoot, task, repository string) ([]worktreeend.Worktree, error) {
	results, err := worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: projectsRoot, Task: task})
	if err != nil {
		return nil, err
	}
	found := make([]worktreeend.Worktree, 0, len(results))
	for _, result := range results {
		if repository != "" && !strings.EqualFold(result.Repository, repository) {
			continue
		}
		found = append(found, worktreeend.Worktree{
			Repository: result.Repository, Path: result.WorktreeDir, Branch: result.Branch,
		})
	}
	return found, nil
}

// streamLinkGuard is the one refusal `wb worktree end` enforces.
//
// It reads both independent signals: a link recorded in stream state, and a
// `go.work` carrying `use` entries. State alone would miss a hand-written
// workspace; the workspace alone would miss an npm link.
type streamLinkGuard struct{ store *streams.Store }

func (guard streamLinkGuard) LiveLinks(worktree string) ([]string, []string, error) {
	var reasons, sanctioned []string
	if guard.store != nil {
		recorded, err := guard.store.LiveLinksForWorktree(worktree)
		if err != nil {
			return nil, nil, err
		}
		for _, link := range recorded {
			reasons = append(reasons, fmt.Sprintf("stream %s: %s links %s (%s)",
				link.Stream, link.Repository, link.Link.Identity, link.Link.Mechanism))
			sanctioned = append(sanctioned, fmt.Sprintf(
				"wb deps propagate local %s --to %s --undo", link.Link.Library, worktree))
		}
	}
	entries, err := streams.GoWorkUseEntries(worktree)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) > 0 {
		reasons = append(reasons, fmt.Sprintf("%s/go.work carries use entries: %s", worktree, strings.Join(entries, ", ")))
		sanctioned = append(sanctioned, "wb deps propagate local --to "+worktree+" --undo")
	}
	return reasons, sanctioned, nil
}

// gitStashCapture preserves uncommitted work as a stash commit.
//
// `git stash push --include-untracked` captures tracked and untracked bytes
// while cleaning the checkout. The stash lives in the repository's COMMON
// directory, which outlives the worktree being removed.
// That is what makes the printed ref recoverable after the checkout is gone.
type gitStashCapture struct{}

func (gitStashCapture) DirtyPaths(ctx context.Context, worktree string) ([]string, error) {
	out, err := runGitIn(ctx, worktree, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) <= 3 {
			continue
		}
		paths = append(paths, strings.TrimSpace(line[3:]))
	}
	return paths, nil
}

func (gitStashCapture) Preserve(ctx context.Context, worktree, message string) (string, error) {
	// `git stash push --include-untracked` is used rather than
	// `stash create` + `store` for two reasons. It captures files Git has
	// never seen — an agent's unfinished work is routinely untracked, and a
	// capture that silently skipped them would be worse than none — and it
	// leaves the working tree CLEAN, which is what lets the existing cleanup
	// transaction retire the checkout at all. A capture that left the tree
	// dirty would be recorded and then refused by cleanup one step later.
	if _, err := runGitIn(ctx, worktree, "stash", "push", "--include-untracked", "--message", message); err != nil {
		return "", fmt.Errorf("capture uncommitted work in %s: %w", worktree, err)
	}
	// refs/stash lives in the repository's common directory, so the captured
	// commit outlives the worktree this verb is about to remove. Resolving it
	// to an immutable SHA means the printed reference still names this exact
	// capture after later stashes push it down the reflog.
	head, err := runGitIn(ctx, worktree, "rev-parse", "refs/stash")
	if err != nil {
		return "", fmt.Errorf("resolve the capture reference in %s: %w", worktree, err)
	}
	return strings.TrimSpace(head), nil
}

// workLogNotes seals the closing statement into the existing Work Log journal.
type workLogNotes struct{}

func (workLogNotes) Seal(worktree, note string) (string, error) {
	return worktrees.AppendPrompt(worktree, worktrees.PromptHeader{
		At: time.Now().UTC(), Source: worktrees.PromptSourceAgent, Slug: "task-ended",
	}, []byte(note))
}

// cleanupRetirer delegates removal to the existing cleanup transaction. It
// invents no removal path of its own — a worktree WB cannot cleanly retire is
// one `wb worktree end` must report, not delete by other means.
type cleanupRetirer struct{}

func (cleanupRetirer) Retire(ctx context.Context, projectsRoot, task, repository, worktree string) error {
	outcome, err := worktrees.Cleanup(ctx, worktrees.CleanupOptions{
		ProjectsRoot: projectsRoot, Task: task, ExactRepository: repository,
		Apply: true, Workers: 1,
	})
	if err != nil {
		return err
	}
	return interpretCleanupRetirement(outcome, repository, worktree)
}

// interpretCleanupRetirement preserves the cleanup report's defensive selection
// and diagnostics without changing the transaction that produced it.
func interpretCleanupRetirement(outcome worktrees.CleanupOutcome, repository, worktree string) error {
	for _, result := range outcome.Results {
		if !strings.EqualFold(result.Repository, repository) {
			continue
		}
		if result.Applied || result.WorktreeGone {
			return nil
		}
		reason := result.Reason
		if reason == "" {
			reason = "cleanup reported no reason"
		}
		return fmt.Errorf("cleanup did not retire %s: %s", worktree, reason)
	}
	return fmt.Errorf("cleanup reported no candidate for %s at %s", repository, worktree)
}

// claimReleaser releases the fleet-wide claim through the existing path.
type claimReleaser struct {
	writer  io.Writer
	release func(string, string, io.Writer)
}

func (releaser claimReleaser) Release(projectsRoot, task string) string {
	releaser.release(projectsRoot, task, releaser.writer)
	return "released through the remote-claim path"
}

func runGitIn(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = console.Env()
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), dir, err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
