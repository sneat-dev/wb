package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newPRCreateCmd() *cobra.Command {
	var format, title, body, bodyFile, base, approvedBy, mergeMethod, message, reviewComment, reviewCommentFile string
	var draft, autoMerge, allowUnfenced, commitStaged, commitAll, land, keep bool
	var timeout time.Duration
	var add, closes []string
	command := &cobra.Command{
		Use:   "create [<worktree|task>]",
		Short: "Push a worktree's branch and open or adopt its pull request",
		Long: `Open a pull request for one worktree without paying for the local suite
'wb worktree merge'/'wb worktree land' run first. CI is the gate: this verb
runs no build, test, or lint pass on its own, and performs no merge unless
--land is given.

Addressed by worktree path or task name; the default is the current
directory, which must be a linked worktree — a canonical clone is refused,
naming 'wb worktree create'. A dirty worktree is refused, listing the
uncommitted paths, unless --commit-staged or --commit-all is given. A branch
with no commit ahead of its base is refused.

--commit-staged commits exactly the index; it refuses when nothing is staged.
--commit-all runs 'git add -A' first (respecting .gitignore) and then
commits, refusing any path that looks like a secret (.env, *.pem, *.key,
id_rsa*, *.p12) and never staging '.worktree.md'. --add <path>[,<path>...]
(repeatable) commits exactly the named paths — 'git add -- <paths>' then
'git commit -m <message> -- <paths>' — so anything else already staged is
left out; a named path must resolve inside the worktree (no '..' escape, no
absolute path outside it) and must match something real, including a tracked
file's own deletion, or it is refused by name; the same secret-path and
'.worktree.md' refusals apply. --commit-staged, --commit-all, and --add are
mutually exclusive, and -m/--message is required with any one of them. Hooks
always run; this never passes --no-verify, and a failing hook stops before
any push.

The branch is pushed through the normal 'git push' path, so hooks run there
too. The pull request is opened through the same idempotent primitive
'wb worktree merge' uses: an already-open pull request for the branch is
adopted, never duplicated.

The default title is the branch's own commit subject (several commits
contribute the most recent one that reads like a real change, or the commit
message given to --commit-staged/--commit-all); the default body is the
commit's own body, or a bullet list of commits for several. A "wip"-shaped
subject never becomes the title. --title overrides the title alone and does
not discard the commit-derived body; pass --body or --body-file for that.

--auto-merge arms GitHub auto-merge immediately, under the same authority
'wb pr land' requires to arm it: a mechanical diff needs no approval, and a
non-mechanical one is refused without --approved-by. Arming is skipped, with
the pull request left open, wherever it would bypass a guard 'wb pr land'
would also refuse to bypass without --allow-unfenced. --draft --auto-merge is
rejected: a draft pull request cannot be merged.

--land goes further and lands the pull request in-process through the same
'wb pr land', in one command: commit, push, open/adopt, wait for checks, and
merge. It implies arming the way 'wb pr land' itself does, so --auto-merge
alongside it is redundant rather than an error, and --approved-by/
--allow-unfenced are meaningful with --land exactly as they are with
--auto-merge. --commit-staged --land with unstaged or untracked changes that
would remain, or --add --land with any change outside --add's own paths, is
refused before anything is committed, because landing retires the worktree
and a leftover dirty checkout cannot be cleaned up afterward; --commit-all,
naming every remaining path in --add, or --keep resolves it. --draft --land is
rejected: a draft pull request cannot be landed.

Adoption looks for the branch's open pull request against ANY base, because
GitHub allows only one open pull request per head branch regardless of base;
one already open against a different base is refused by naming it, rather
than silently retargeted.

Exit codes: 0 created/adopted (or landed, with --land), 1 a finding (for
example auto-merge could not be armed, or CI is not ready), 2 a guard
refused.`,
		Example: `# Open a pull request from the current worktree
wb pr create

# Commit everything, open the pull request, and land it once green
wb pr create --commit-all -m "feat: the change" --land --approved-by review.md

# Commit only the named paths, open the pull request, and land it once green
wb pr create --add pkg/x.go,pkg/x_test.go -m "feat: the change" --land --approved-by review.md

# Open it, then land it separately once green
wb pr create
wb pr land sneat-co/sneat-go#1041

# Open a draft with an explicit title
wb pr create --draft --title "feat: the change"

# Arm auto-merge on a mechanical bump
wb pr create --auto-merge

# Arm auto-merge on a reviewed change
wb pr create --auto-merge --approved-by review-1041.md

# Machine-readable envelope
wb pr create --format json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if strings.TrimSpace(body) != "" && strings.TrimSpace(bodyFile) != "" {
				return usageError("--body and --body-file are mutually exclusive")
			}
			if draft && autoMerge {
				return usageError("--draft and --auto-merge are mutually exclusive: a draft pull request cannot be merged")
			}
			if draft && land {
				return usageError("--draft and --land are mutually exclusive: a draft pull request cannot be landed")
			}
			if !autoMerge && !land && command.Flags().Changed("approved-by") {
				return usageError("--approved-by is only meaningful with --auto-merge or --land")
			}
			if !autoMerge && !land && command.Flags().Changed("allow-unfenced") {
				return usageError("--allow-unfenced is only meaningful with --auto-merge or --land")
			}
			commitModes := 0
			for _, active := range []bool{commitStaged, commitAll, len(add) > 0} {
				if active {
					commitModes++
				}
			}
			if commitModes > 1 {
				return usageError("--commit-staged, --commit-all, and --add are mutually exclusive")
			}
			if (commitStaged || commitAll || len(add) > 0) && strings.TrimSpace(message) == "" {
				return usageError("--commit-staged/--commit-all/--add require -m/--message")
			}
			if strings.TrimSpace(reviewComment) != "" && strings.TrimSpace(reviewCommentFile) != "" {
				return usageError("--review-comment and --review-comment-file are mutually exclusive")
			}
			// Round 3, MAJOR fix: without --land, nothing in this command
			// ever reads --review-comment/--review-comment-file - --land is
			// the only path that posts the identity form's review comment.
			// Accepting either flag here and silently dropping it is worse
			// than refusing: the caller would believe a review was recorded
			// when --auto-merge alone armed with nothing but the identity
			// string itself.
			if !land && (command.Flags().Changed("review-comment") || command.Flags().Changed("review-comment-file")) {
				return usageError("--review-comment/--review-comment-file are only meaningful with --land; " +
					"--auto-merge alone never posts the review comment, so it would be silently ignored")
			}
			closesIssues, closesErr := parseIssueNumbers(splitCommaSeparated(closes))
			if closesErr != nil {
				return usageError(closesErr.Error())
			}
			worktreeArg := ""
			if len(args) > 0 {
				worktreeArg = args[0]
			}
			// #615: never add issue numbers to the body silently — print
			// whatever the task's own original prompt names as a suggestion,
			// leaving --closes as the only thing that acts on it.
			if suggested := suggestedClosesFromWorktreePrompt(command.Context(), worktreeArg); len(suggested) > 0 && len(closesIssues) == 0 {
				fmt.Fprintf(command.ErrOrStderr(), "suggestion: this task's prompt names %s; pass --closes to link them\n",
					formatSuggestedIssues(suggested))
			}
			var landOptions *orchestrate.PullRequestLandOptions
			if land {
				interactive := console.Interactive(command.ErrOrStderr(), false)
				progress := newCIWaitProgress(progressOutput(command.ErrOrStderr(), interactive), true)
				landOptions = &orchestrate.PullRequestLandOptions{
					ProjectsRoot:      projectsRoot,
					Keep:              keep,
					ApprovedBy:        approvedBy,
					ReviewComment:     reviewComment,
					ReviewCommentFile: reviewCommentFile,
					MergeMethod:       mergeMethod,
					AllowUnfenced:     allowUnfenced,
					Slice:             timeout,
					CheckPollInterval: orchestrate.DefaultCheckPollInterval,
					Progress:          progress.report,
					OperationProgress: progress.operationReporter("pr create --land"),
					Lane:              landingLaneGuardRequest("wb pr create --land", "", false),
					CheckoutUpdated:   lifecycleCheckoutUpdated(command.ErrOrStderr()),
				}
			}
			var lane orchestrate.LaneGuardRequest
			if autoMerge && !land {
				lane = landingLaneGuardRequest("wb pr create --auto-merge", "", false)
			}
			result, createErr := orchestrate.CreatePullRequest(command.Context(), orchestrate.PullRequestCreateOptions{
				Worktree: worktreeArg, ProjectsRoot: projectsRoot,
				Title: title, Body: body, BodyFile: bodyFile, Draft: draft, Base: base,
				Add: add, CommitStaged: commitStaged, CommitAll: commitAll, Message: message, Closes: closesIssues,
				AutoMerge: autoMerge, ApprovedBy: approvedBy, AllowUnfenced: allowUnfenced, MergeMethod: mergeMethod, Lane: lane,
				Land: land, LandOptions: landOptions,
				LinkPreflight: refuseLinkedRepositoryWorktrees,
				// The repository is not known until the worktree's manifest is
				// read inside CreatePullRequest itself, unlike `wb pr land`,
				// which already has it as a CLI argument — so this hands over
				// the same repository-scoped-stream resolver `wb pr land`
				// uses, rather than a repository resolved too early to be right.
				EventsForRepository: landingEventLog,
			})
			// The envelope is printed whatever createErr is: `result` is
			// initialized on the invocation's very first line and carries
			// CommittedPaths/LandResult however far the call got, so an
			// error after the commit step (a push failure, a failed arm, ...)
			// still reports a finding instead of a bare, structure-less error.
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(result); err != nil {
					return err
				}
			} else if result.LandResult != nil {
				if err := printPullRequestLand(command, *result.LandResult); err != nil {
					return err
				}
			} else if err := printPullRequestCreate(command, result); err != nil {
				return err
			}
			if createErr != nil {
				return &exitError{code: exitFindings, message: createErr.Error()}
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
	command.Flags().StringVar(&title, "title", "", "override the default commit-derived title")
	command.Flags().StringVar(&body, "body", "", "literal pull-request body text; mutually exclusive with --body-file")
	command.Flags().StringVar(&bodyFile, "body-file", "", "path to the pull-request body; mutually exclusive with --body")
	command.Flags().BoolVar(&draft, "draft", false, "open the pull request as a draft; mutually exclusive with --auto-merge")
	command.Flags().StringVar(&base, "base", "", "override the pull request's target branch; default is the worktree's own recorded base")
	command.Flags().BoolVar(&commitStaged, "commit-staged", false, "commit exactly the index before pushing; mutually exclusive with --commit-all")
	command.Flags().BoolVar(&commitAll, "commit-all", false, "git add -A then commit before pushing, refusing any path that looks like a secret")
	command.Flags().StringSliceVar(&add, "add", nil, "commit exactly these paths (repeatable, or comma-separated); mutually exclusive with --commit-staged/--commit-all")
	command.Flags().StringVarP(&message, "message", "m", "", "the commit message; required with --commit-staged/--commit-all")
	command.Flags().BoolVar(&autoMerge, "auto-merge", false, "arm GitHub auto-merge immediately after the pull request is created or adopted")
	command.Flags().BoolVar(&land, "land", false, "land the pull request in-process through wb pr land once it is created or adopted")
	command.Flags().BoolVar(&keep, "keep", false, "with --land: retain the task's worktree and claim instead of retiring them")
	command.Flags().DurationVar(&timeout, "timeout", defaultCIWaitSlice, "with --land: total foreground wait budget")
	command.Flags().StringVar(&approvedBy, "approved-by", "", "the recorded review that authorizes --auto-merge/--land on a non-mechanical change: a review file, a comment URL, or (with --land) a reviewer identity {model}[@{harness}[@{session}]] plus --review-comment/--review-comment-file")
	command.Flags().StringVar(&reviewComment, "review-comment", "", "with --land: the review text for a reviewer-identity --approved-by; mutually exclusive with --review-comment-file")
	command.Flags().StringVar(&reviewCommentFile, "review-comment-file", "", "with --land: path to the review text for a reviewer-identity --approved-by; mutually exclusive with --review-comment")
	command.Flags().StringSliceVar(&closes, "closes", nil, "issue number(s) this pull request closes (repeatable, or comma-separated); writes one 'Closes #N' line per issue at the top of the body")
	command.Flags().BoolVar(&allowUnfenced, "allow-unfenced", false, "with --auto-merge/--land: arm on a target with no server-enforced strict up-to-date policy")
	command.Flags().StringVar(&mergeMethod, "merge-method", "merge", "with --auto-merge/--land: merge (default), squash, or rebase")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	setDiscoveryTerms(command, "open create pull request pr push branch worktree adopt draft auto merge cheap fast no build no test commit land")
	return command
}

func printPullRequestCreate(command *cobra.Command, result orchestrate.PullRequestCreateResult) error {
	out := command.OutOrStdout()
	switch result.Outcome {
	case orchestrate.CreateSuccess, orchestrate.CreateFindings:
		if len(result.CommittedPaths) > 0 {
			if _, err := fmt.Fprintf(out, "committed %s\n", strings.Join(result.CommittedPaths, ", ")); err != nil {
				return err
			}
		}
		if result.Repository != "" && result.PullRequest != 0 {
			verb := "created"
			if result.Adopted {
				verb = "adopted"
			}
			if _, err := fmt.Fprintf(out, "%s %s#%d %s\n", verb, result.Repository, result.PullRequest, result.Title); err != nil {
				return err
			}
		}
		if result.AutoMergeArmed {
			if _, err := fmt.Fprintln(out, "auto-merge armed"); err != nil {
				return err
			}
		} else if result.AutoMergeReason != "" {
			if _, err := fmt.Fprintf(out, "not armed: %s\n", result.AutoMergeReason); err != nil {
				return err
			}
		}
		if result.NextCommand != "" {
			if _, err := fmt.Fprintf(out, "next: %s\n", result.NextCommand); err != nil {
				return err
			}
		}
		if result.Outcome == orchestrate.CreateFindings && result.Reason != "" {
			if _, err := fmt.Fprintf(out, "finding: %s\n", result.Reason); err != nil {
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

// parseIssueNumbers converts --closes's comma-separated/repeated values into
// issue numbers, refusing anything that is not a positive integer by name
// rather than silently dropping it.
func parseIssueNumbers(values []string) ([]int, error) {
	issues := make([]int, 0, len(values))
	// Round 3, minor 6: dedupe here, in first-seen order, so
	// "--closes 5,5,6" (or two "--closes 5" repeats) never writes "Closes
	// #5" twice at the top of the body.
	seen := map[int]bool{}
	for _, value := range values {
		number, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || number <= 0 {
			return nil, fmt.Errorf("--closes %q is not a positive issue number", value)
		}
		if seen[number] {
			continue
		}
		seen[number] = true
		issues = append(issues, number)
	}
	return issues, nil
}

// suggestedClosesFromWorktreePrompt reads the worktree's own original
// prompt (Work Log) and returns whatever issue numbers it names (#615). Any
// failure to resolve the worktree or load its Work Log is silent here: this
// is a courtesy suggestion, never a requirement, and must not turn into a
// usage error for a worktree that simply has no recorded prompt.
func suggestedClosesFromWorktreePrompt(ctx context.Context, worktreeArg string) []int {
	worktree, err := orchestrate.ResolvePullRequestCreateWorktree(ctx, projectsRoot, worktreeArgOrCurrent(worktreeArg))
	if err != nil {
		return nil
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{
		ProjectsRoot: projectsRoot, Worktree: worktree, IncludePromptBodies: true,
	})
	if err != nil || view.OriginalPrompt == nil {
		return nil
	}
	return orchestrate.SuggestClosesFromPrompt(view.OriginalPrompt.Body)
}

func worktreeArgOrCurrent(worktreeArg string) string {
	if strings.TrimSpace(worktreeArg) == "" {
		return "."
	}
	return worktreeArg
}

func formatSuggestedIssues(issues []int) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, fmt.Sprintf("#%d", issue))
	}
	return strings.Join(parts, ", ")
}
