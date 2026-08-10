package worktrees

import (
	"context"
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// AbortDisposition makes an unfinished effort legible instead of leaving an
// ambiguous directory behind. Handoff and not_landed retain the worktree for a
// later claim; discarded is the only disposition that removes local Git state.
type AbortDisposition string

const (
	AbortHandoff   AbortDisposition = "handoff"
	AbortNotLanded AbortDisposition = "not_landed"
	AbortDiscarded AbortDisposition = "discarded"
)

type AbortOptions struct {
	ProjectsRoot string
	Task         string
	Base         string
	Disposition  AbortDisposition
	Apply        bool
}

type AbortResult struct {
	ListResult
	Disposition   AbortDisposition `json:"disposition"`
	Eligible      bool             `json:"eligible"`
	Applied       bool             `json:"applied"`
	WorktreeGone  bool             `json:"worktree_gone"`
	BranchDeleted bool             `json:"branch_deleted"`
	Reason        string           `json:"reason,omitempty"`
}

// Abort seals every Work Log in a coordinated task. It is the deliberate
// escape hatch for unused or interrupted claims which cannot meet the merged
// PR evidence required by Cleanup. --apply never destroys resumable work:
// only an explicit discarded disposition removes a clean linked checkout and
// its exact local branch ref. The private archive/outbox is written first.
func Abort(ctx context.Context, options AbortOptions) ([]AbortResult, error) {
	projectsRoot, task, base, _, err := normalizeListOptions(ListOptions{ProjectsRoot: options.ProjectsRoot, Task: options.Task, Base: options.Base})
	if err != nil {
		return nil, err
	}
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}
	if options.Disposition != AbortHandoff && options.Disposition != AbortNotLanded && options.Disposition != AbortDiscarded {
		return nil, fmt.Errorf("disposition must be handoff, not_landed, or discarded")
	}
	listed, err := ListWithDiagnostics(ctx, ListOptions{ProjectsRoot: projectsRoot, Task: task, Base: base})
	if err != nil {
		return nil, err
	}
	if len(listed.Results) == 0 {
		return nil, fmt.Errorf("WB worktree task %q was not found", task)
	}
	results := make([]AbortResult, len(listed.Results))
	for i, entry := range listed.Results {
		eligible := entry.Clean && !entry.Locked
		reason := ""
		if entry.Locked {
			reason = "task is locked by an active or interrupted operation"
		} else if !entry.Clean {
			reason = "worktree has local changes; checkpoint or hand off without aborting"
		}
		results[i] = AbortResult{ListResult: entry, Disposition: options.Disposition, Eligible: eligible, Reason: reason}
	}
	if len(listed.Diagnostics) > 0 {
		for i := range results {
			results[i].Eligible = false
			results[i].Reason = "task has malformed worktree candidate: " + listed.Diagnostics[0].Path
		}
	}
	if !options.Apply {
		return results, nil
	}
	for _, result := range results {
		if !result.Eligible {
			return results, fmt.Errorf("task %q cannot be aborted safely: %s", task, result.Reason)
		}
	}
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return results, err
	}
	for i := range results {
		result := &results[i]
		if err := sealWorkLogForRecycle(resolution.Write.Home, result.WorktreeDir, result.HeadSHA, string(options.Disposition)); err != nil {
			return results, fmt.Errorf("seal aborted work log for %s: %w", result.Repository, err)
		}
		if options.Disposition == AbortDiscarded {
			if _, err := git(ctx, result.CanonicalDir, "worktree", "remove", result.WorktreeDir); err != nil {
				return results, fmt.Errorf("remove discarded worktree %s: %w", result.WorktreeDir, err)
			}
			result.WorktreeGone = true
			if _, err := git(ctx, result.CanonicalDir, "update-ref", "-d", "refs/heads/"+result.Branch, result.HeadSHA); err != nil {
				return results, fmt.Errorf("delete discarded branch %s: %w", result.Branch, err)
			}
			result.BranchDeleted = true
		}
		result.Applied = true
	}
	return results, nil
}

func (d AbortDisposition) String() string { return strings.TrimSpace(string(d)) }
