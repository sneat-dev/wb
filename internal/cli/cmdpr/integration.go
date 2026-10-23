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
	"github.com/sneat-dev/wb/internal/quality"
)

// IntegrationDependencies are the integration verbs' orchestration entry
// points.
type IntegrationDependencies struct {
	Add   func(context.Context, orchestrate.IntegrationOptions) (orchestrate.IntegrationResult, error)
	Merge func(context.Context, orchestrate.IntegrationOptions) (orchestrate.IntegrationResult, error)
}

// DefaultIntegrationDependencies wires the production verbs.
func DefaultIntegrationDependencies() IntegrationDependencies {
	return IntegrationDependencies{Add: orchestrate.AddToBatch, Merge: orchestrate.MergeIntegration}
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
recipe (.wb/quality.yaml; --checks lint,test,build,spec selects, default
lint,test,build) on the result and pushes it to origin/<integration> without
force only when every check passed. No CI runs on that push, and the
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
	command.AddCommand(add)
	return command
}

// NewIntegration constructs `wb integration` and its verbs.
func NewIntegration(runtime shared.Runtime, deps IntegrationDependencies) *cobra.Command {
	command := &cobra.Command{Use: "integration", Short: "Merge, check and push reviewed lanes into an integration branch", Long: integrationLong}
	discovery(command, "integration branch local check push reviewed lane merge commit")
	merge := newIntegrationVerb(runtime, "merge <owner/repository> <integration> [<branch@sha=review-url>...]",
		"Merge reviewed lanes, run the local checks, and push without force", integrationLong, false, deps.Merge)
	command.AddCommand(merge)
	return command
}

// newIntegrationVerb builds one verb. A batch verb names the integration
// branch with --target and needs a pin; an integration verb takes it as the
// second argument.
func newIntegrationVerb(runtime shared.Runtime, use, short, long string, batch bool, run func(context.Context, orchestrate.IntegrationOptions) (orchestrate.IntegrationResult, error)) *cobra.Command {
	var target, worktree, format, checks string
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
				parsed, err := quality.ParseChecks(checks)
				if err != nil {
					return runtime.ExitError(shared.ExitUsage, err.Error())
				}
				options.Integration, options.Pins, options.Checks = args[1], args[2:], parsed
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
		command.Flags().StringVar(&checks, "checks", "", "local checks to run before pushing: lint,test,build,spec (default lint,test,build)")
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
