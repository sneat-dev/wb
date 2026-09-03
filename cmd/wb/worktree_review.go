package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newWorktreeReviewCmd() *cobra.Command {
	var format, sha, task, from, base, repositoryFlag string
	var agent, runtime, model, cli, provider, initiator, mode string
	var ttl time.Duration
	command := &cobra.Command{
		Use:   "review [owner/repository#number]",
		Short: "Create a tracked, claimed checkout of a pull request to review",
		Long: `Create the checkout a review needs, as a WB worktree.

A review checkout is the largest single source of permanent worktree debt on
this fleet, and the cause is mechanical. A reviewer needs the pull request's
head, so 'gh pr checkout' or 'git worktree add --detach' is the natural move —
and from that moment WB has no manifest, no claim, no owner and no Work Log for
the checkout, which means no WB verb can ever retire it. One night's sweep found
ten of them, four to seventeen hours old, every pull request already merged, and
nothing in WB able to remove one.

This verb closes that at the source. The checkout is created exactly the way
every other WB worktree is, so it arrives tracked: an immutable manifest, a Work
Log claim, an owner and a TTL — and 'wb worktree gc' retires it once the work it
reviews has landed, without anyone having to remember anything. The manifest is
also what makes the heartbeat work: a checkout made with 'git worktree add' has
no journal to write one into, so nothing can tell whether anyone is using it.

REVIEWING WORK THAT HAS NO PULL REQUEST is the ordinary case, not the exception.
Under the local model an agent's work is absorbed into its stream without ever
opening one, so '--from <branch-or-commit>' is how most reviews start:

    wb worktree review --from agent/fix-login --repo sneat-co/sneat-go

The ref is resolved in the repository's canonical clone. A ref that does not
resolve is refused rather than guessed at.

It sits on a branch, not a detached HEAD. A detached checkout is precisely the
shape WB cannot retire, and creating one on purpose would reproduce the defect
this verb exists to remove. The branch is local, named review/<owner>-<repo>-<n>,
and is never pushed.

The task name is derived from the pull request, so a second reviewer of the same
pull request collides with the first rather than creating an indistinguishable
second checkout.

A pull request that is not open is refused: its head is a historical fact rather
than a live one, and reviewing it is deliberate. --sha reviews an exact commit
and is the sanctioned way to do that.

Finish with 'wb worktree review end <task>', which seals the Work Log and
removes the checkout even when it is dirty — a review leaves scratch files, and
that must not be a reason a checkout survives forever.`,
		Example: `# Review a local branch that never opened a pull request
wb worktree review --from agent/fix-login --repo sneat-co/sneat-go --model claude-opus-5

# Review a pull request
wb worktree review sneat-co/sneat-go#1041 --model claude-opus-5

# Review an exact commit, including on a closed pull request
wb worktree review sneat-co/sneat-go#1041 --sha 4f2a1c9 --model claude-opus-5

# Finish
wb worktree review end review-sneat-co-sneat-go-1041`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			repository, number := "", ""
			if len(args) == 1 {
				var err error
				repository, number, err = splitPullRequestSelector(args[0])
				if err != nil {
					return &exitError{code: exitUsage, message: err.Error()}
				}
			}
			if from != "" {
				// A local review names its repository the way every other
				// repository-scoped verb does, or takes the one the current
				// checkout is in.
				if repository == "" {
					repository = repositoryFlag
				}
				if repository == "" {
					derived, deriveErr := worktrees.OriginSlug(command.Context(), ".")
					if deriveErr != nil {
						return &exitError{code: exitUsage, message: "reviewing a local ref needs a repository: pass --repo owner/repository, or run from inside one"}
					}
					repository = derived
				}
			}
			if repository == "" {
				return &exitError{code: exitUsage, message: "give a pull request (owner/repository#number) or --from <branch-or-commit>"}
			}
			// The same admission rule `wb worktree create` applies: a review
			// checkout is a mutation, and WB records who made it.
			if mode != "" && mode != "auto" && mode != "agent" && mode != "manual" {
				return &exitError{code: exitUsage, message: fmt.Sprintf("unsupported execution mode %q; use auto, agent, or manual", mode)}
			}
			sessionRequired := mode == "agent" ||
				(mode == "auto" && (strings.TrimSpace(agent) != "" || strings.TrimSpace(runtime) != ""))
			if mode == "manual" && strings.TrimSpace(initiator) == "" {
				return &exitError{code: exitUsage, message: "manual execution mode requires --initiator so the non-agent mutation is auditable"}
			}
			result, err := orchestrate.CreateReviewWorktree(command.Context(), orchestrate.WorktreeReviewOptions{
				Repository:      repository,
				PullRequest:     number,
				ProjectsRoot:    projectsRoot,
				Task:            task,
				From:            from,
				Base:            base,
				SHA:             sha,
				TTL:             ttl,
				SessionRequired: sessionRequired,
				WorkLog: worktrees.WorkLogOptions{
					AgentID: agent, AgentRuntime: runtime, Model: model,
					CLI: cli, Provider: provider, Initiator: initiator,
				},
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(result); err != nil {
					return err
				}
			} else if err := printWorktreeReview(command, result); err != nil {
				return err
			}
			if result.Outcome != "success" {
				return &exitError{code: exitUsage, message: result.Reason + "; resolve with: " + result.SanctionedCommand}
			}
			worktreeCreateAutoClaim(defaultRemoteDeps(), false, projectsRoot, result.Task, remoteClaimWriter(command))
			return nil
		},
	}
	command.Flags().StringVar(&from, "from", "", "review a local branch or commit — the ordinary path for work that never opened a pull request")
	command.Flags().StringVar(&base, "base", "", "with --from, the branch the work targets (default main)")
	command.Flags().StringVar(&repositoryFlag, "repo", "", "with --from, the repository to review in; defaults to the current checkout's")
	command.Flags().StringVar(&sha, "sha", "", "review this exact commit instead of the pull request's current head")
	command.Flags().StringVar(&task, "task", "", "override the derived task name")
	command.Flags().DurationVar(&ttl, "ttl", orchestrate.DefaultReviewTTL, "how long this checkout is expected to stay useful")
	command.Flags().StringVar(&agent, "agent", "", "agent identity")
	command.Flags().StringVar(&runtime, "agent-runtime", "", "agent runtime, e.g. codex or claude")
	command.Flags().StringVar(&model, "model", "", "required exact model identifier, or explicit unknown; WB never guesses")
	command.Flags().StringVar(&cli, "cli", "", "optional invoking CLI/client identifier")
	command.Flags().StringVar(&provider, "provider", "", "optional routing/billing provider identifier, never a credential")
	command.Flags().StringVar(&initiator, "initiator", "", "human or agent that started the review")
	command.Flags().StringVar(&mode, "mode", "auto", "execution mode: auto, agent (requires a live registered session), or manual (requires --initiator)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	setDiscoveryTerms(command, "review pull request checkout tracked claimed detached gh pr checkout reviewer inspect diff")
	command.AddCommand(newWorktreeReviewEndCmd())
	return command
}

func newWorktreeReviewEndCmd() *cobra.Command {
	var format string
	var apply bool
	command := &cobra.Command{
		Use:   "end <task>",
		Short: "Finish a review and retire its checkout, dirty or not",
		Long: `Seal a review checkout's Work Log and remove it.

A review leaves scratch files — notes, a coverage profile, an experiment — and
none of that is work anyone intends to keep. Ending the review therefore removes
the checkout even when it is dirty, after capturing the uncommitted bytes into
the private Work Log archive, so nothing is lost and nothing survives forever
because someone left a file behind.

This is 'wb worktree abort <task> --disposition discarded' with the review's
name on it: the same audited transaction, the same durable capture, the same
seal. The default is a dry-run plan.`,
		Example: `wb worktree review end review-sneat-co-sneat-go-1041 --apply`,
		Args:    cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			results, err := worktrees.Abort(command.Context(), worktrees.AbortOptions{
				ProjectsRoot: projectsRoot, Task: args[0], Filter: filterFlag,
				Disposition: worktrees.AbortDiscarded, Apply: apply, DeleteRemote: true,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(results); err != nil {
					return err
				}
			} else {
				for _, result := range results {
					state := "would end"
					if result.Applied {
						state = "ended"
					}
					if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s %s\n", state, result.Task, result.Repository); err != nil {
						return err
					}
				}
			}
			if apply {
				tryAutoRelease(defaultRemoteDeps(), projectsRoot, args[0], remoteClaimWriter(command))
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "remove the checkout instead of planning")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func printWorktreeReview(command *cobra.Command, result orchestrate.WorktreeReviewResult) error {
	out := command.OutOrStdout()
	if result.Outcome != "success" {
		if _, err := fmt.Fprintf(out, "%s: %s\n", result.Outcome, result.Reason); err != nil {
			return err
		}
		_, err := fmt.Fprintf(out, "resolve with: %s\n", result.SanctionedCommand)
		return err
	}
	subject := result.Repository + " " + result.LocalRef
	if result.PullRequest != 0 {
		subject = fmt.Sprintf("%s#%d", result.Repository, result.PullRequest)
	}
	if _, err := fmt.Fprintf(out, "reviewing %s %s\n", subject, result.Title); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "created %s (%s at %s, ttl %s)\n",
		result.WorktreeDir, result.Branch, shortSHAForDisplay(result.HeadSHA),
		(time.Duration(result.TTLSeconds) * time.Second).String())
	return err
}
