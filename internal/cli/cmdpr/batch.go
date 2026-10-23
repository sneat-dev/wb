package cmdpr

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
)

// BatchDependencies are the batch verbs' orchestration entry points.
type BatchDependencies struct {
	Create func(context.Context, orchestrate.BatchOptions) (orchestrate.BatchResult, error)
	Resume func(context.Context, orchestrate.BatchOptions) (orchestrate.BatchResult, error)
}

// DefaultBatchDependencies wires the production verbs.
func DefaultBatchDependencies() BatchDependencies {
	return BatchDependencies{
		Create: orchestrate.CreatePullRequestBatch,
		Resume: orchestrate.ResumePullRequestBatch,
	}
}

const batchLong = `Batch independently reviewed pull requests into one integration pull request.

A batch runs in a WB worktree cut from the integration target on a branch
named <target>-batch-<n>:

  wb worktree create public-demo-batch-1 datatug/datatug-apps \
    --base public-demo --branch public-demo-batch-1 --model <model> --original-prompt-file -

Each source is pinned as <number>@<sha>=<review-comment-url>: the pull request,
the exact head that was reviewed, and the review comment whose last
"Reviewed-Head:" line names that head. Write pins without a leading "#" (it
starts a shell comment). WB verifies every pin before touching Git, merges
each pinned head with its own --no-ff merge commit in the order given, never
rebases, squashes or force-pushes, and opens exactly one pull request whose
body carries the batch state. A conflict stops the batch with the files
listed; commit the resolution and run "wb pr batch resume".

Landing a batch needs an independent review of the batch head ending
"Reviewed-Head: <batch head>", even when no source conflicted: semantic
conflicts produce no textual one, so a clean batch is still reviewed.

Exit codes: 0 done, 1 a finding (a conflict, an unrecorded merge), 2 refused.`

// NewBatch constructs `wb pr batch` and its verbs.
func NewBatch(runtime shared.Runtime, deps BatchDependencies) *cobra.Command {
	command := &cobra.Command{Use: "batch", Short: "Batch reviewed pull requests into one integration pull request", Long: batchLong}
	discovery(command, "batch integration branch reviewed heads merge commits one pull request delta conflict resolution")
	command.AddCommand(
		newBatchVerb(runtime, "create", "create <owner/repository> --target <branch> <pr@sha=review-url>...", "Verify pins and build a new batch with one merge commit per pin", true, true, deps.Create),
		newBatchVerb(runtime, "resume", "resume <owner/repository> --target <branch>", "Accept a committed conflict resolution and continue the batch", false, false, deps.Resume),
	)
	return command
}

func newBatchVerb(runtime shared.Runtime, _, use, short string, takesPins, needsPins bool, run func(context.Context, orchestrate.BatchOptions) (orchestrate.BatchResult, error)) *cobra.Command {
	var target, worktree, format string
	minimum := 1
	if needsPins {
		minimum = 2
	}
	command := &cobra.Command{
		Use: use, Short: short,
		Args: func(command *cobra.Command, args []string) error {
			if len(args) < minimum || (!takesPins && len(args) > 1) {
				return runtime.ExitError(shared.ExitUsage, "usage: wb pr batch "+use)
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if strings.TrimSpace(target) == "" {
				return runtime.ExitError(shared.ExitUsage, "--target <integration branch> is required")
			}
			if strings.TrimSpace(worktree) == "" {
				worktree, _ = os.Getwd()
			}
			result, err := run(command.Context(), orchestrate.BatchOptions{
				Repository: args[0], Target: target, Worktree: worktree, Pins: args[1:],
				ProjectsRoot: runtime.Flags().ProjectsRoot,
			})
			if err != nil {
				return err
			}
			if err := printBatch(command.OutOrStdout(), format, result); err != nil {
				return err
			}
			if code := result.ExitCode(); code != 0 {
				return runtime.ExitError(code, result.Reason)
			}
			return nil
		},
	}
	command.Flags().StringVar(&target, "target", "", "integration branch the batch lands into (required)")
	command.Flags().StringVar(&worktree, "worktree", "", "batch worktree (default: the current directory)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func printBatch(out io.Writer, format string, result orchestrate.BatchResult) error {
	if format == "json" {
		raw, _ := orchestrate.MarshalBatchResult(result) // a plain struct always marshals
		_, err := fmt.Fprintln(out, string(raw))
		return err
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s: %s\n", result.Outcome, result.Reason)
	if result.RefusalCode != "" {
		fmt.Fprintf(&text, "refusal: %s\n", result.RefusalCode)
	}
	for _, issue := range result.Refusals {
		fmt.Fprintf(&text, "  %s %s: %s\n", issue.Code, issue.Source, issue.Reason)
	}
	if result.PullRequest != "" {
		fmt.Fprintf(&text, "pull request: %s\n", result.PullRequest)
	}
	if result.State != nil {
		for i, step := range result.State.Steps {
			source := "refresh " + result.State.Target
			if step.Kind == orchestrate.BatchStepSource {
				source = fmt.Sprintf("#%d", step.PullRequest)
			}
			fmt.Fprintf(&text, "  %d. %s %s %s", i+1, source, step.State, step.Head)
			if len(step.ConflictPaths) > 0 {
				fmt.Fprintf(&text, " conflicts: %s", strings.Join(step.ConflictPaths, ", "))
			}
			text.WriteString("\n")
		}
	}
	if result.Command != "" {
		fmt.Fprintf(&text, "next: %s\n", result.Command)
	}
	_, err := io.WriteString(out, text.String())
	return err
}
