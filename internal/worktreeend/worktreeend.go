// Package worktreeend closes one task's worktrees: the verb every lane
// contract already tells an agent to finish with.
//
// It exists because "the agent is done" had no sanctioned expression. An agent
// that finished either left its checkout for a later sweep — the residue
// `wb worktree gc` has to clean up — or hand-rolled `git worktree remove`,
// which loses the claim, the Work Log seal and any uncommitted work in one
// step. `wb worktree end` is the one verb that does all three correctly.
//
// It owns no new mechanics. Removal is the existing cleanup transaction, the
// claim release is the existing remote-claim path, and the closing note is the
// existing Work Log prompt journal. What this package adds is the order, the
// refusal, and the guarantee that uncommitted work is captured *before*
// anything is removed.
//
// Implements: dependency-streams#req:sessions-and-tasks-have-explicit-ends,
// dependency-streams#req:merge-refuses-a-linked-worktree (its end-of-task half).
package worktreeend

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Options is one `wb worktree end` invocation.
type Options struct {
	Task string
	// Repository narrows a coordinated task to one owner/repository.
	Repository string
	// Note is recorded in the Work Log as the task's closing statement.
	Note string
	// Apply performs the retirement. Without it nothing is changed and the
	// report says exactly what would happen — including which worktrees carry
	// uncommitted work that would be captured.
	Apply bool
	// KeepCapture stops the capture from being taken. It is not a bypass of
	// anything: it is for a caller that has already preserved the work and
	// does not want a second copy.
	KeepCapture bool
}

// MemberResult is one worktree's retirement.
type MemberResult struct {
	Repository string `json:"repository"`
	Worktree   string `json:"worktree"`
	// Dirty lists the uncommitted paths found before anything was removed.
	Dirty []string `json:"dirty,omitempty"`
	// CaptureRef is where the uncommitted work was preserved. It is printed
	// so the operator can recover it; a capture nobody can find is not a
	// capture.
	CaptureRef string `json:"capture_ref,omitempty"`
	// UnpushedCommits is how many local commits the checkout held.
	UnpushedCommits int `json:"unpushed_commits,omitempty"`
	// ParkPush is the SHA park published, empty when there was nothing
	// unpushed.
	ParkPush string `json:"park_push,omitempty"`
	NotePath string `json:"note_path,omitempty"`
	Removed  bool   `json:"removed"`
	Action   string `json:"action"`
	Detail   string `json:"detail,omitempty"`
}

// Result is the whole invocation's report.
type Result struct {
	Task         string         `json:"task"`
	Applied      bool           `json:"applied"`
	Members      []MemberResult `json:"members"`
	ClaimOutcome string         `json:"claim_outcome,omitempty"`
	Errors       []string       `json:"errors,omitempty"`
}

// Failed reports whether any worktree could not be retired.
func (result Result) Failed() bool {
	if len(result.Errors) > 0 {
		return true
	}
	for _, member := range result.Members {
		if member.Action == "failed" {
			return true
		}
	}
	return false
}

// Refusal is a guard that fired, carrying the command that satisfies it.
type Refusal struct {
	Code       string
	Message    string
	Sanctioned []string
}

func (refusal *Refusal) Error() string {
	if len(refusal.Sanctioned) == 0 {
		return refusal.Message
	}
	return refusal.Message + "; run: " + strings.Join(refusal.Sanctioned, " or ")
}

// RefusalLiveLink is the one condition that stops a task from ending: a
// worktree still building against an unpublished library working tree.
const RefusalLiveLink = "live-link"

// Worktree is one checkout belonging to the task.
type Worktree struct {
	Repository string
	Path       string
	Branch     string
}

// Inventory lists the task's worktrees.
type Inventory interface {
	Worktrees(ctx context.Context, projectsRoot, task, repository string) ([]Worktree, error)
}

// LinkGuard answers whether a worktree still holds a live local link.
type LinkGuard interface {
	// LiveLinks returns a human-readable reason per live link, and the
	// command that clears each. An empty result means the worktree is free
	// to be retired.
	LiveLinks(worktree string) (reasons []string, sanctioned []string, err error)
}

// Capture preserves uncommitted work before anything is removed.
type Capture interface {
	// DirtyPaths lists uncommitted paths, tracked and untracked.
	DirtyPaths(ctx context.Context, worktree string) ([]string, error)
	// Preserve captures the uncommitted work and returns a reference the
	// operator can recover it from. It must not depend on the worktree
	// surviving: the whole point is that removal follows.
	Preserve(ctx context.Context, worktree, message string) (ref string, err error)
}

// Notes seals the task's closing statement into the Work Log.
type Notes interface {
	Seal(worktree, note string) (path string, err error)
}

// Parker pushes unpushed work before a checkout is retired.
//
// `pushes-are-justified-and-counted` names park as one of exactly four
// triggers, and it is the one that exists precisely for this moment: a
// checkout being retired with local commits nobody else can see. A capture to a
// local stash survives the worktree but not the machine, so park pushes.
type Parker interface {
	// UnpushedCommits counts commits the remote does not carry.
	UnpushedCommits(ctx context.Context, worktree, branch string) (int, error)
	// Push publishes the branch with trigger `park`, returning the pushed SHA.
	// It must verify the ref it pushed rather than trusting an exit code.
	Push(ctx context.Context, worktree, branch, reason string) (sha string, err error)
}

// Retirer runs the existing cleanup transaction.
type Retirer interface {
	Retire(ctx context.Context, projectsRoot, task, repository, worktree string) error
}

// Claims releases the task's fleet-wide claim once every worktree is retired.
type Claims interface {
	Release(projectsRoot, task string) string
}

// Engine performs `wb worktree end` against injected ports.
type Engine struct {
	ProjectsRoot string
	Inventory    Inventory
	Links        LinkGuard
	Capture      Capture
	Parker       Parker
	Notes        Notes
	Retirer      Retirer
	Claims       Claims
	Now          func() time.Time
}

func (engine *Engine) now() time.Time {
	if engine.Now == nil {
		return time.Now().UTC()
	}
	return engine.Now().UTC()
}

// End closes a task.
//
// The order is the contract:
//
//  1. refuse while any worktree holds a live local link — landing or
//     discarding a checkout that builds against an unpublished working tree
//     is the one thing end must never do silently;
//  2. capture uncommitted work and print where it went, BEFORE any removal;
//  3. seal the closing note into the Work Log;
//  4. retire each worktree through the existing cleanup transaction;
//  5. release the fleet-wide claim, but only once every worktree is gone.
//
// A dirty worktree is not a refusal. Refusing one would leave the agent with
// exactly the choice this verb exists to remove: hand-roll the removal, or
// leave residue. Capturing first makes retiring it safe.
func (engine *Engine) End(ctx context.Context, options Options) (Result, error) {
	if strings.TrimSpace(options.Task) == "" {
		return Result{}, fmt.Errorf("a task name is required")
	}
	worktrees, err := engine.Inventory.Worktrees(ctx, engine.ProjectsRoot, options.Task, options.Repository)
	if err != nil {
		return Result{}, err
	}
	if len(worktrees) == 0 {
		return Result{}, fmt.Errorf("task %q has no worktrees under %s", options.Task, engine.ProjectsRoot)
	}
	result := Result{Task: options.Task, Applied: options.Apply}

	// Fence first, over every worktree, before a single side effect.
	var reasons, sanctioned []string
	for _, worktree := range worktrees {
		if engine.Links == nil {
			break
		}
		found, commands, err := engine.Links.LiveLinks(worktree.Path)
		if err != nil {
			return result, err
		}
		reasons = append(reasons, found...)
		sanctioned = append(sanctioned, commands...)
	}
	if len(reasons) > 0 {
		return result, &Refusal{
			Code: RefusalLiveLink,
			Message: fmt.Sprintf("task %q still holds a live local link, so its worktrees build against an unpublished working tree: %s",
				options.Task, strings.Join(reasons, "; ")),
			Sanctioned: uniqueStrings(sanctioned),
		}
	}

	for _, worktree := range worktrees {
		result.Members = append(result.Members, engine.endWorktree(ctx, options, worktree))
	}
	if !options.Apply {
		return result, nil
	}
	retired := true
	for _, member := range result.Members {
		if !member.Removed {
			retired = false
		}
	}
	// The claim is released only when nothing is left holding it. Releasing
	// it while a worktree survives would advertise the task as free while its
	// checkout is still on disk.
	if retired && engine.Claims != nil {
		result.ClaimOutcome = engine.Claims.Release(engine.ProjectsRoot, options.Task)
	} else if !retired {
		result.ClaimOutcome = "kept: not every worktree was retired"
	}
	return result, nil
}

func (engine *Engine) endWorktree(ctx context.Context, options Options, worktree Worktree) MemberResult {
	member := MemberResult{Repository: worktree.Repository, Worktree: worktree.Path}
	dirty, err := engine.Capture.DirtyPaths(ctx, worktree.Path)
	if err != nil {
		member.Action, member.Detail = "failed", err.Error()
		return member
	}
	member.Dirty = dirty

	if !options.Apply {
		member.Action = "would-end"
		if engine.Parker != nil && worktree.Branch != "" {
			if unpushed, err := engine.Parker.UnpushedCommits(ctx, worktree.Path, worktree.Branch); err == nil {
				member.UnpushedCommits = unpushed
			}
		}
		var notes []string
		if member.UnpushedCommits > 0 {
			notes = append(notes, fmt.Sprintf("%d unpushed commit(s) would be pushed with trigger park", member.UnpushedCommits))
		}
		if len(dirty) > 0 && !options.KeepCapture {
			notes = append(notes, fmt.Sprintf("%d uncommitted path(s) would be captured before removal", len(dirty)))
		}
		member.Detail = strings.Join(notes, "; ")
		return member
	}

	// Unpushed COMMITS go to the remote before the checkout is retired.
	// A stash capture survives the worktree but not the machine, so work that
	// is already committed is pushed under the `park` trigger — this is the
	// one moment park exists for.
	if engine.Parker != nil && worktree.Branch != "" {
		unpushed, err := engine.Parker.UnpushedCommits(ctx, worktree.Path, worktree.Branch)
		if err != nil {
			member.Action, member.Detail = "failed", "could not establish what is unpushed, so nothing was removed: "+err.Error()
			return member
		}
		member.UnpushedCommits = unpushed
		if unpushed > 0 {
			sha, err := engine.Parker.Push(ctx, worktree.Path, worktree.Branch, "park: "+options.Task)
			if err != nil {
				// Refusing to retire silently is the point: removing a
				// checkout whose commits exist nowhere else loses them.
				member.Action = "failed"
				member.Detail = fmt.Sprintf(
					"%d unpushed commit(s) could not be pushed, so the checkout was NOT retired: %v", unpushed, err)
				return member
			}
			member.ParkPush = sha
		}
	}

	// Capture strictly before removal. A capture taken afterwards is not a
	// capture; it is a description of something that no longer exists.
	if len(dirty) > 0 && !options.KeepCapture {
		message := fmt.Sprintf("wb worktree end %s at %s", options.Task, engine.now().Format(time.RFC3339))
		ref, err := engine.Capture.Preserve(ctx, worktree.Path, message)
		if err != nil {
			member.Action, member.Detail = "failed", "could not capture uncommitted work, so nothing was removed: "+err.Error()
			return member
		}
		member.CaptureRef = ref
	}

	if engine.Notes != nil {
		note := options.Note
		if strings.TrimSpace(note) == "" {
			note = "Task ended with `wb worktree end " + options.Task + "`."
		}
		if member.CaptureRef != "" {
			note += "\nUncommitted work captured at " + member.CaptureRef + "."
		}
		path, err := engine.Notes.Seal(worktree.Path, note)
		if err != nil {
			// A note that could not be sealed must not stop the retirement:
			// the capture already exists, and leaving the worktree behind
			// because of a journal write is the residue this verb removes.
			member.Detail = strings.TrimSpace(member.Detail + " note not sealed: " + err.Error())
		} else {
			member.NotePath = path
		}
	}

	if err := engine.Retirer.Retire(ctx, engine.ProjectsRoot, options.Task, worktree.Repository, worktree.Path); err != nil {
		member.Action = "failed"
		member.Detail = strings.TrimSpace(member.Detail + " " + err.Error())
		return member
	}
	member.Removed = true
	member.Action = "ended"
	return member
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}
