package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
	"github.com/sneat-dev/wb/internal/worktreeend"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newWorktreeEndCmd() *cobra.Command {
	var (
		repository, note, format string
		apply, keepCapture       bool
	)
	command := &cobra.Command{
		Use:   "end <task>",
		Short: "Close a task: capture uncommitted work, seal a note, retire the worktrees, release the claim",
		Long: `End is how an agent finishes. It is the closing half of 'wb worktree create'
and the last line of every lane contract.

In order, and the order is the contract:

  1. refuse while any worktree holds a live local link — a checkout that builds
     against an unpublished library working tree must never be retired silently
  2. capture uncommitted work and print where it went, BEFORE anything is
     removed
  3. seal a closing note into the Work Log
  4. retire each worktree through the existing 'wb worktree cleanup' transaction
  5. release the fleet-wide claim, but only once every worktree is gone

A dirty worktree is NOT a refusal. Refusing one would leave exactly the choice
this verb exists to remove: hand-roll the removal, or leave residue. The
uncommitted work is captured as a git stash commit in the repository the
worktree was cut from — it survives the worktree's removal — and the exact ref
is printed. Recover it with 'git stash apply <ref>' or 'git show <ref>'.

Retirement itself is the existing cleanup transaction, so its own guards still
apply: an unmerged branch is refused by cleanup with its reason, not silently
deleted here.

The default is a dry-run plan; --apply performs the retirement.`,
		Example: `# See what ending would do
wb worktree end improve-login

# Close it
wb worktree end improve-login --apply

# Close one repository of a coordinated task
wb worktree end improve-login --repo acme/app --apply --note "landed in #412"`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			store, err := streams.Open(projectsRoot)
			if err != nil {
				return err
			}
			engine := &worktreeend.Engine{
				ProjectsRoot: projectsRoot,
				Inventory:    worktreeInventory{},
				Links:        streamLinkGuard{store: store},
				Capture:      gitStashCapture{},
				Parker:       parkPusher{events: streamEventSink{log: store.EventLog(parkStreamFor(store, args[0]))}},
				Notes:        workLogNotes{},
				Retirer:      cleanupRetirer{},
				Claims:       claimReleaser{writer: command.ErrOrStderr()},
			}
			result, err := engine.End(command.Context(), worktreeend.Options{
				Task: args[0], Repository: repository, Note: note,
				Apply: apply, KeepCapture: keepCapture,
			})
			if err != nil {
				// A guard that fired is exit 2 with the command that
				// satisfies it; a failure is exit 1. errors.As rather than a
				// type assertion, so a refusal still reaches the caller after
				// any later wrapping.
				var refusal *worktreeend.Refusal
				if errors.As(err, &refusal) {
					return &exitError{code: exitUsage, message: refusal.Error()}
				}
				return err
			}
			if err := printWorktreeEnd(command, format, result); err != nil {
				return err
			}
			if result.Failed() {
				return &exitError{code: exitFindings, message: "wb worktree end reported findings; see the report above"}
			}
			return nil
		},
	}
	command.Flags().StringVar(&repository, "repo", "", "narrow a coordinated task to one owner/repository")
	command.Flags().StringVar(&note, "note", "", "closing statement sealed into the Work Log")
	command.Flags().BoolVar(&apply, "apply", false, "perform the retirement; without it nothing is changed")
	command.Flags().BoolVar(&keepCapture, "no-capture", false, "do not capture uncommitted work (only when it is already preserved elsewhere)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	setDiscoveryTerms(command, "worktree end finish close task done retire cleanup claim release stash capture lane contract")
	return command
}

func printWorktreeEnd(command *cobra.Command, format string, result worktreeend.Result) error {
	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	out := command.OutOrStdout()
	verb := "would end"
	if result.Applied {
		verb = "ended"
	}
	if _, err := fmt.Fprintf(out, "%s task %s\n", verb, result.Task); err != nil {
		return err
	}
	for _, member := range result.Members {
		if _, err := fmt.Fprintf(out, "  %-28s %-10s %s\n", member.Repository, member.Action, member.Worktree); err != nil {
			return err
		}
		if len(member.Dirty) > 0 {
			if _, err := fmt.Fprintf(out, "    uncommitted: %s\n", strings.Join(member.Dirty, ", ")); err != nil {
				return err
			}
		}
		if member.ParkPush != "" {
			if _, err := fmt.Fprintf(out, "    pushed %d local commit(s) with trigger park: %s\n", member.UnpushedCommits, member.ParkPush); err != nil {
				return err
			}
		}
		if member.CaptureRef != "" {
			if _, err := fmt.Fprintf(out, "    captured at %s — recover with `git stash apply %s`\n", member.CaptureRef, member.CaptureRef); err != nil {
				return err
			}
		}
		if member.Detail != "" {
			if _, err := fmt.Fprintf(out, "    ! %s\n", member.Detail); err != nil {
				return err
			}
		}
	}
	if result.ClaimOutcome != "" {
		if _, err := fmt.Fprintf(out, "  claim: %s\n", result.ClaimOutcome); err != nil {
			return err
		}
	}
	if !result.Applied {
		if _, err := fmt.Fprintln(out, "nothing was changed; re-run with --apply"); err != nil {
			return err
		}
	}
	return nil
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
// `git stash create` builds the commit without touching the stash reflog or
// the working tree; `git stash store` then anchors it under refs/stash in the
// repository's COMMON directory, which outlives the worktree being removed.
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
	writer interface{ Write([]byte) (int, error) }
}

func (releaser claimReleaser) Release(projectsRoot, task string) string {
	tryAutoRelease(defaultRemoteDeps(), projectsRoot, task, releaser.writer)
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

// parkPusher publishes unpushed commits before a checkout is retired.
//
// `pushes-are-justified-and-counted` names park as one of four triggers, and
// this is the moment it exists for: a stash capture survives the worktree but
// not the machine, so committed work that exists nowhere else is pushed first.
type parkPusher struct{ events streamEventSink }

func (parker parkPusher) UnpushedCommits(ctx context.Context, worktree, branch string) (int, error) {
	// A branch with no remote counterpart is entirely unpushed, which is the
	// normal state for an agent branch rather than an error.
	if _, err := runGitIn(ctx, worktree, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch); err != nil {
		out, countErr := runGitIn(ctx, worktree, "rev-list", "--count", branch)
		if countErr != nil {
			return 0, nil
		}
		return strconv.Atoi(strings.TrimSpace(out))
	}
	out, err := runGitIn(ctx, worktree, "rev-list", "--count", "origin/"+branch+".."+branch)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

func (parker parkPusher) Push(ctx context.Context, worktree, branch, reason string) (string, error) {
	local, err := runGitIn(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := runGitIn(ctx, worktree, "push", "--set-upstream", "origin", branch); err != nil {
		return "", err
	}
	// The push exit code is not evidence the intended commit landed.
	if _, err := runGitIn(ctx, worktree, "fetch", "--quiet", "origin", branch); err != nil {
		return "", err
	}
	remote, err := runGitIn(ctx, worktree, "rev-parse", "refs/remotes/origin/"+branch)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(remote) != strings.TrimSpace(local) {
		return "", fmt.Errorf("pushed %s but origin/%s is %s", strings.TrimSpace(local), branch, strings.TrimSpace(remote))
	}
	_ = parker.events.Append(streamsync.Event{
		Verb: "worktree end", Phase: "push", Outcome: "success",
		Detail:   reason,
		Evidence: map[string]string{"trigger": string(streamsync.TriggerPark), "reason": reason, "branch": branch, "head": strings.TrimSpace(local)},
	})
	return strings.TrimSpace(local), nil
}

// parkStreamFor names the log a park push is recorded in: the stream holding
// the task where there is one, the fleet log otherwise.
func parkStreamFor(store *streams.Store, task string) string {
	all, _, err := store.List()
	if err != nil {
		return task
	}
	for _, stream := range all {
		if stream.Open() && stream.Name == task {
			return stream.Name
		}
	}
	return task
}
