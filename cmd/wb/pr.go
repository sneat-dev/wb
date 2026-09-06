package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/spf13/cobra"
)

func newPRCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "pr",
		Short: "Land and inspect pull requests as one deterministic operation",
	}
	command.AddCommand(newPRLandCmd())
	return command
}

func newPRLandCmd() *cobra.Command {
	var format, approvedBy, subject, reason, mergeMethod string
	var keepCommits []string
	var keep, allowUnfenced, nonInteractive bool
	var pollInterval, totalTimeout time.Duration
	command := &cobra.Command{
		Use:   "land <owner/repository#number>",
		Short: "Verify, land, and tidy up after one pull request",
		Long: `Land one pull request and leave nothing behind.

This replaces the sequence an operator or an agent otherwise runs by hand —
view the pull request, poll its checks until they settle, merge it, verify the
merge reached the base, delete the branch, retire the worktree — with one verb
that performs and verifies all of it. Every one of those steps is a place the
hand-run sequence goes wrong, and one of them is measured: the merge stage of
'wb worktree merge' broke on the installed gh, so operators used raw
'gh pr merge', and the opt-in cleanup that should have retired the worktree
never ran. Sixty abandoned checkouts followed.

Every GitHub call goes through 'gh api' with an endpoint, and pagination
follows GitHub's own link header, so nothing here needs a newer gh than the one
installed.

CLEANUP IS THE DEFAULT. The task's worktree is retired and its claim released
unless --keep is passed. An opt-in cleanup is a cleanup that does not happen.

SQUASH IS THE DEFAULT, and the squash message AGGREGATES the branch: the
subject is the pull request's title — GitHub otherwise substitutes the branch's
first commit subject, which is how a "wip(...)" message lands on main and
cannot be corrected without rewriting history — and the body carries the pull
request's summary, one line per source commit, the pull request number, and the
review that authorized it.

--keep-commits <sha>[,<sha>...] --reason "<text>" is the exception: wb rebuilds
the branch so those commits land as their own commits, in order, with the rest
squashed into one aggregated commit that records the reason. --reason is
mandatory, because a commit standing alone in the history of a default branch
has to say why. Each kept commit must build on its own; one that does not is
refused, naming a smaller set, because a commit that does not build is not a
place anyone can bisect to.

REVIEW. A mechanical dependency bump — a diff touching only go.mod, go.sum,
package.json dependency fields, pnpm-lock.yaml, pnpm-workspace.yaml — lands on
its batch verification with no review ledger entry. The classification is made from the diff,
never from the title, author or labels: a bot-titled bump that
also edits a source file is not mechanical, and is refused without
--approved-by <review-file-or-comment-url>.

Exit codes: 0 landed, 1 the work is not ready (checks red or pending, landing
unverified), 2 a guard refused.`,
		Example: `# Land a green dependency bump, retiring its worktree
wb pr land sneat-co/sneat-go#1041

# Land a reviewed change
wb pr land sneat-co/sneat-go#1041 --approved-by review-sneat-go-1041.md

# Keep one commit in its own place in the history
wb pr land sneat-co/sneat-go#1041 --approved-by review.md \
  --keep-commits 4f2a1c9 --reason "the migration must be revertable on its own"

# Machine-readable envelope
wb pr land sneat-co/sneat-go#1041 --format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			repository, number, err := splitPullRequestSelector(args[0])
			if err != nil {
				return &exitError{code: exitUsage, message: err.Error()}
			}
			interactive := console.Interactive(command.ErrOrStderr(), nonInteractive)
			progress := newCIWaitProgress(progressOutput(command.ErrOrStderr(), interactive), true)
			progress.start(repository, number, "", "")
			// The landing guard runs before anything else, including the
			// GitHub read: a worktree of this repository still building against
			// an unpublished tree makes every check observation meaningless.
			progress.live.update("pr land: local link preflight: " + repository + ": started")
			if err := refuseLinkedRepositoryWorktrees(repository); err != nil {
				progress.finishOperation("pr land: local link preflight: failed: " + err.Error())
				return err
			}
			progress.live.update("pr land: local link preflight: " + repository + ": completed")
			events, streamName := landingEventLog(repository)
			result, err := orchestrate.LandPullRequest(command.Context(), orchestrate.PullRequestLandOptions{
				Repository:        repository,
				PullRequest:       number,
				ProjectsRoot:      projectsRoot,
				Keep:              keep,
				ApprovedBy:        approvedBy,
				MergeMethod:       mergeMethod,
				Subject:           subject,
				KeepCommits:       splitCommaSeparated(keepCommits),
				Reason:            reason,
				AllowUnfenced:     allowUnfenced,
				Slice:             totalTimeout,
				CheckPollInterval: pollInterval,
				Progress:          progress.report,
				OperationProgress: progress.operationReporter("pr land"),
				Events:            events,
				Stream:            streamName,
			})
			if err != nil {
				progress.fail(err)
				return err
			}
			progress.finishOperation("pr land: " + string(result.Outcome))
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(result); err != nil {
					return err
				}
			} else if err := printPullRequestLand(command, result); err != nil {
				return err
			}
			// The savings footer is an interactive courtesy: under
			// --non-interactive the same figures are in the envelope, and a
			// script must not have to strip prose to read them.
			if format == "text" && !nonInteractive && console.IsTerminal(command.OutOrStdout()) {
				if _, err := fmt.Fprintln(command.OutOrStdout(), result.FooterLine()); err != nil {
					return err
				}
			}
			switch result.ExitCode() {
			case 0:
				return nil
			case exitUsage:
				return &exitError{code: exitUsage, message: result.Reason + "; resolve with: " + result.SanctionedCommand}
			default:
				return &exitError{code: exitFindings, message: result.Reason}
			}
		},
	}
	command.Flags().BoolVar(&keep, "keep", false, "retain the task's worktree and claim instead of retiring them")
	command.Flags().StringVar(&approvedBy, "approved-by", "", "the recorded review that authorized a non-mechanical change: a review file or a comment URL")
	command.Flags().StringVar(&subject, "subject", "", "override the squash subject (default: the pull request title)")
	command.Flags().StringSliceVar(&keepCommits, "keep-commits", nil, "source commits that must land as their own commits; requires --reason")
	command.Flags().StringVar(&reason, "reason", "", "why the kept commits stand alone; recorded in the aggregated commit and the receipt")
	command.Flags().StringVar(&mergeMethod, "merge-method", "squash", "squash, merge, or rebase")
	command.Flags().BoolVar(&allowUnfenced, "allow-unfenced", false, "land on observed checks where the target has no server-enforced strict up-to-date policy")
	command.Flags().DurationVar(&pollInterval, "poll-interval", orchestrate.DefaultCheckPollInterval, "interval between check observations")
	command.Flags().DurationVar(&totalTimeout, "timeout", defaultCIWaitSlice, "total foreground wait budget; WB uses bounded resumable CI observation slices internally")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&nonInteractive, "non-interactive", false, "never use a terminal UI, and suppress the savings footer")
	setDiscoveryTerms(command, "land merge pull request pr squash aggregate keep commits cleanup worktree claim checks green approve review bump")
	return markLandingGuard(command, landingGuardByPullRequest)
}

// splitPullRequestSelector reads owner/repository#number, the way an operator
// already writes a pull request when talking about one.
func splitPullRequestSelector(selector string) (repository, number string, err error) {
	value := strings.TrimSpace(selector)
	repository, rest, found := strings.Cut(value, "#")
	if !found {
		if url := strings.TrimSpace(value); strings.Contains(url, "/pull/") {
			repository, urlErr := orchestrate.RepositoryFromPullRequestURL(url)
			if urlErr != nil {
				return "", "", urlErr
			}
			number, numberErr := orchestrate.PullRequestNumber(url)
			return repository, number, numberErr
		}
		return "", "", fmt.Errorf("selector %q must be owner/repository#number or a pull request URL", selector)
	}
	repository = strings.TrimSpace(repository)
	if strings.Count(repository, "/") != 1 || strings.HasPrefix(repository, "/") || strings.HasSuffix(repository, "/") {
		return "", "", fmt.Errorf("selector %q must name owner/repository before the #", selector)
	}
	number, err = orchestrate.PullRequestNumber(rest)
	return repository, number, err
}

func splitCommaSeparated(values []string) []string {
	split := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				split = append(split, trimmed)
			}
		}
	}
	return split
}

func printPullRequestLand(command *cobra.Command, result orchestrate.PullRequestLandResult) error {
	out := command.OutOrStdout()
	classification := "needs review"
	if result.Mechanical {
		classification = "mechanical bump"
	}
	if _, err := fmt.Fprintf(out, "%s#%d %s (%s)\n", result.Repository, result.PullRequest, result.Title, classification); err != nil {
		return err
	}
	switch result.Outcome {
	case orchestrate.LandSuccess:
		if _, err := fmt.Fprintf(out, "landed %s on %s\n", shortSHAForDisplay(result.MergeSHA), result.BaseRef); err != nil {
			return err
		}
		if result.BranchDeleted {
			if _, err := fmt.Fprintf(out, "retired origin/%s\n", result.HeadRef); err != nil {
				return err
			}
		}
		for _, task := range result.CleanedTasks {
			if _, err := fmt.Fprintf(out, "retired worktree for task %s\n", task); err != nil {
				return err
			}
		}
		if result.Kept {
			if _, err := fmt.Fprintln(out, "kept the worktree (--keep)"); err != nil {
				return err
			}
		}
		for _, commit := range result.Commits {
			if !commit.Kept {
				continue
			}
			if _, err := fmt.Fprintf(out, "kept commit %s %s\n", shortSHAForDisplay(commit.SourceSHA), commit.Subject); err != nil {
				return err
			}
		}
	default:
		if _, err := fmt.Fprintf(out, "%s: %s\n", result.Outcome, result.Reason); err != nil {
			return err
		}
		if result.RefusalCode != "" {
			if _, err := fmt.Fprintf(out, "refusal: %s\n", result.RefusalCode); err != nil {
				return err
			}
		}
		if result.SanctionedCommand != "" {
			if _, err := fmt.Fprintf(out, "resolve with: %s\n", result.SanctionedCommand); err != nil {
				return err
			}
		}
	}
	return nil
}

func shortSHAForDisplay(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// landingEventLog resolves where this landing's event belongs.
//
// A landing inside a stream belongs to that stream's log, which is what makes
// `wb report stream` able to show the landing beside the work that produced it.
// A landing outside every stream still writes an event — the analytics exist to
// measure verbs, not only streams — into the fleet log.
func landingEventLog(repository string) (streams.EventAppender, string) {
	store, err := streams.Open(projectsRoot)
	if err != nil {
		return streams.DiscardEvents{}, ""
	}
	if stream, found, _, streamErr := store.RepositoryStream(repository); streamErr == nil && found {
		return store.EventLog(stream.Name), stream.Name
	}
	// Outside every stream the event still belongs somewhere: the analytics
	// exist to measure verbs, not only streams, and a landing that leaves no
	// record is a landing the report cannot count.
	return store.EventLog(fleetEventLogName), ""
}

// fleetEventLogName is the log for verbs that ran outside any stream. It is a
// name no stream can take: stream names are validated as path segments and
// cannot contain a dot.
const fleetEventLogName = ".fleet"
