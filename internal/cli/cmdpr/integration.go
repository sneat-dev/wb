package cmdpr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
)

// IntegrationDependencies are the integration verbs' orchestration entry
// points.
type IntegrationDependencies struct {
	AdoptLegacy func(context.Context, orchestrate.LegacyAdoptOptions) (orchestrate.LegacyAdoptResult, error)
	Add         func(context.Context, orchestrate.IntegrationOptions) (orchestrate.IntegrationResult, error)
	Merge       func(context.Context, orchestrate.IntegrationOptions) (orchestrate.IntegrationResult, error)
	Delta       func(context.Context, orchestrate.PromoteOptions) (orchestrate.PromoteResult, error)
	Promote     func(context.Context, orchestrate.PromoteOptions) (orchestrate.PromoteResult, error)
}

// DefaultIntegrationDependencies wires the production verbs.
func DefaultIntegrationDependencies() IntegrationDependencies {
	return IntegrationDependencies{Add: orchestrate.AddToBatch, Merge: orchestrate.MergeIntegration,
		Delta: orchestrate.DeltaBatch, Promote: orchestrate.PromoteIntegration}
}

const pinHelp = `Each lane is pinned as <branch>@<sha>=<commit-comment-url>: the lane's pushed
branch, the exact commit that was reviewed, and the GitHub commit comment on
that commit (https://github.com/<owner>/<repo>/commit/<sha>#commitcomment-<id>)
whose last "Reviewed-Head:" line names it. Lanes open no pull request; a
reviewer posts the review as a commit comment, for example:

  gh api repos/<owner>/<repo>/commits/<sha>/comments -F body=@review.md

WB verifies every pin before touching Git, then merges each pinned commit with
its own --no-ff merge commit whose trailers (Wb-Lane, Wb-Review,
Wb-Review-Digest, Wb-Reviewer, Wb-Merged-By) are the only record. It never
rebases, squashes or force-pushes. A conflict stops with the files listed:
resolve them, run "git commit --no-edit" (it keeps the trailers), and rerun the
same command; merged pins are skipped.

Run it in a WB worktree cut from the integration branch:

  wb worktree create <task> <owner/repo> --base <integration> --model <model> --original-prompt-file -`

const batchLong = `Merge reviewed lane commits into an integration branch locally, without pushing.

` + pinHelp + `

"wb integration merge" does the same, then runs the local checks and pushes.

Exit codes: 0 done, 1 a conflict, 2 refused.`

const integrationLong = `Merge reviewed lane commits into an integration branch, check locally, push.

` + pinHelp + `

After the merges, "wb integration merge" runs the repository's local-check
recipe on the result: --checks, else .wb/quality.yaml integration_checks, else
lint, test and build plus typecheck where a project defines it. It pushes the
checked commit to origin/<integration> without force only when every check
passed. When the integration branch does not
contain --to (default the repository's default branch), it first merges --to
as a recorded refresh (Wb-Refresh), so the promotion is never behind its
target. No CI runs on that push, and the
integration branch needs no ruleset: full CI runs on the one integration pull
request into main. A rerun with nothing new runs no check.

Exit codes: 0 pushed or nothing to push, 1 a conflict or a failed check, 2
refused (a bad pin, a moved integration branch).`

// NewBatch constructs `wb pr batch` and its verbs.
func NewBatch(runtime shared.Runtime, deps IntegrationDependencies) *cobra.Command {
	command := &cobra.Command{Use: "batch", Short: "Merge reviewed lane commits into an integration branch", Long: batchLong}
	discovery(command, "batch integration branch reviewed lane commit merge commits trailers")
	add := newIntegrationVerb(runtime, "add <owner/repository> --target <integration> <branch@sha=review-url>...",
		"Verify pins and merge each lane commit locally with its own merge commit", batchLong, true, deps.Add)
	delta := newPromoteVerb(runtime, "delta <owner/repository> --target <integration> [--to <branch>]",
		"Print only the integration delta a promotion reviewer reads", deltaLong, true, deps.Delta)
	command.AddCommand(add, delta)
	return command
}

// NewIntegration constructs `wb integration` and its verbs.
func NewIntegration(runtime shared.Runtime, deps IntegrationDependencies) *cobra.Command {
	command := &cobra.Command{Use: "integration", Short: "Merge, check and push reviewed lanes into an integration branch", Long: integrationLong}
	discovery(command, "integration branch local check push reviewed lane merge commit")
	merge := newIntegrationVerb(runtime, "merge <owner/repository> <integration> [<branch@sha=review-url>...]",
		"Merge reviewed lanes, run the local checks, and push without force", integrationLong, false, deps.Merge)
	promote := newPromoteVerb(runtime, "promote <owner/repository> <integration> [--to <branch>]",
		"Re-prove every lane merge and open or refresh the one pull request to main", promoteLong, false, deps.Promote)
	command.AddCommand(merge, promote, newLegacyAdopt(runtime, deps))
	return command
}

const deltaLong = `Print only the integration delta of an integration branch against its target.

The delta is what a promotion reviewer reads: every merge's conflict
resolution (its committed tree against the conflict-free merge of its
parents), every non-merge fix commit, and the full diff of any merge whose
trailers do not bind a still-valid review (an unrecorded merge, exit 1).
Content reviewed at a lane SHA, or brought in from the target, never appears.
An empty delta still needs the promotion review, because semantic conflicts
produce no textual one: --overlap adds each lane's own diff of every file more
than one lane changed. --to defaults to the repository's default branch.

Exit codes: 0 printed, 1 an unrecorded merge, 2 refused.`

const promoteLong = `Re-prove an integration branch and open or refresh its one pull request.

Every merge on the integration branch's first-parent history that its target
(--to, default the repository's default branch) lacks must bind its second
parent to a still-valid lane review through its Wb-* trailers, or bring in
target history; otherwise promotion refuses batch-unrecorded-merge. The pull
request body lists every lane and carries the promotion marker, so
"wb pr land" lands it only with a fresh, independent review of its exact head
(--approved-by), never updating the branch server-side or arming auto-merge,
under the repository's own merge policy with full CI. A rerun with nothing new
changes nothing.

Exit codes: 0 opened, refreshed or nothing to promote, 2 refused.`

func newPromoteVerb(runtime shared.Runtime, use, short, long string, batch bool, run func(context.Context, orchestrate.PromoteOptions) (orchestrate.PromoteResult, error)) *cobra.Command {
	var target, to, worktree, format string
	var overlap bool
	var legacyRecords []string
	command := &cobra.Command{
		Use: use, Short: short, Long: long,
		Args: func(command *cobra.Command, args []string) error {
			if want := map[bool]int{true: 1, false: 2}[batch]; len(args) != want {
				return runtime.ExitError(shared.ExitUsage, "usage: wb "+command.Parent().Name()+" "+use)
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			options := orchestrate.PromoteOptions{Repository: args[0], To: to, Worktree: worktree, Overlap: overlap, LegacyRecords: legacyRecords}
			if batch {
				if strings.TrimSpace(target) == "" {
					return runtime.ExitError(shared.ExitUsage, "--target <integration branch> is required")
				}
				options.Integration = target
			} else {
				options.Integration = args[1]
			}
			if strings.TrimSpace(options.Worktree) == "" {
				options.Worktree, _ = os.Getwd()
			}
			result, err := run(command.Context(), options)
			if err != nil {
				return err
			}
			if format == "json" {
				raw, _ := json.MarshalIndent(result, "", "  ") // a plain struct always marshals
				_, err = fmt.Fprintln(command.OutOrStdout(), string(raw))
			} else {
				_, err = io.WriteString(command.OutOrStdout(), orchestrate.PromoteText(result))
			}
			if err != nil {
				return err
			}
			if code := result.ExitCode(); code != 0 {
				return runtime.ExitError(code, result.Reason)
			}
			return nil
		},
	}
	if batch {
		command.Flags().StringVar(&target, "target", "", "integration branch (required)")
		command.Flags().BoolVar(&overlap, "overlap", false, "also print each lane's own diff of every file more than one lane changed")
	}
	command.Flags().StringSliceVar(&legacyRecords, "legacy-record", nil, "exact archived provenance <commit-comment-url>=sha256:<digest> (repeatable; does not approve code)")
	command.Flags().StringVar(&to, "to", "", "promotion target (default: the repository's default branch)")
	command.Flags().StringVar(&worktree, "worktree", "", "checkout to read from (default: the current directory)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

// newIntegrationVerb builds one verb. A batch verb names the integration
// branch with --target and needs a pin; an integration verb takes it as the
// second argument.
func newIntegrationVerb(runtime shared.Runtime, use, short, long string, batch bool, run func(context.Context, orchestrate.IntegrationOptions) (orchestrate.IntegrationResult, error)) *cobra.Command {
	var target, worktree, format, checks, to string
	minimum := 2
	command := &cobra.Command{
		Use: use, Short: short, Long: long,
		Args: func(command *cobra.Command, args []string) error {
			if len(args) < minimum {
				return runtime.ExitError(shared.ExitUsage, "usage: wb "+command.Parent().Name()+" "+use)
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			options := orchestrate.IntegrationOptions{Repository: args[0], Worktree: worktree, ProjectsRoot: runtime.Flags().ProjectsRoot}
			if batch {
				if strings.TrimSpace(target) == "" {
					return runtime.ExitError(shared.ExitUsage, "--target <integration branch> is required")
				}
				options.Integration, options.Pins = target, args[1:]
			} else {
				var parsed []quality.Check
				if command.Flags().Changed("checks") {
					var err error
					if parsed, err = quality.ParseChecks(checks); err != nil {
						return runtime.ExitError(shared.ExitUsage, err.Error())
					}
				}
				options.Integration, options.Pins, options.Checks, options.To = args[1], args[2:], parsed, to
			}
			if strings.TrimSpace(options.Worktree) == "" {
				options.Worktree, _ = os.Getwd()
			}
			result, err := run(command.Context(), options)
			if err != nil {
				return err
			}
			if err := printIntegration(command.OutOrStdout(), format, result); err != nil {
				return err
			}
			if code := result.ExitCode(); code != 0 {
				return runtime.ExitError(code, result.Reason)
			}
			return nil
		},
	}
	if batch {
		command.Flags().StringVar(&target, "target", "", "integration branch the lanes merge into (required)")
	} else {
		command.Flags().StringVar(&checks, "checks", "", "local checks to run before pushing: lint,test,build,typecheck,spec (default: the repository's integration_checks, else lint,test,build plus typecheck where defined)")
		command.Flags().StringVar(&to, "to", "", "promotion target to refresh from when the integration branch lacks it (default: the repository's default branch)")
	}
	command.Flags().StringVar(&worktree, "worktree", "", "integration worktree (default: the current directory)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func printIntegration(out io.Writer, format string, result orchestrate.IntegrationResult) error {
	if format == "json" {
		raw, _ := orchestrate.MarshalIntegrationResult(result) // a plain struct always marshals
		_, err := fmt.Fprintln(out, string(raw))
		return err
	}
	_, err := io.WriteString(out, orchestrate.IntegrationText(result))
	return err
}
