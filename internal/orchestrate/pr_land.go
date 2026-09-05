package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// PullRequestLand is the deterministic replacement for the sequence an operator
// or an agent otherwise performs by hand: view the pull request, poll its
// checks until they settle, merge it, verify the merge reached the base, delete
// the branch, and retire the worktree that produced it.
//
// Every one of those steps is a place the hand-run sequence goes wrong, and the
// measured failure is specific: the merge stage of the existing verb broke on
// the installed `gh`, so operators used raw `gh pr merge`, and the opt-in
// cleanup that should have retired the worktree simply never ran. Cleanup is
// therefore the default here, `--keep` is the only way out of it, and every
// GitHub call goes through `gh api` so no newer client is required.

// LandRefusal codes are the machine-readable half of a refusal. A caller
// branches on these rather than on prose.
const (
	LandRefusalDraft             = "draft-pull-request"
	LandRefusalNotOpen           = "pull-request-not-open"
	LandRefusalLocked            = "pull-request-locked"
	LandRefusalHeadMoved         = "head-moved"
	LandRefusalNotMergeable      = "not-mergeable"
	LandRefusalUnapprovedPatch   = "unapproved-patch-set"
	LandRefusalChecksPending     = "checks-pending"
	LandRefusalChecksFailed      = "checks-failed"
	LandRefusalMergeRejected     = "merge-rejected"
	LandRefusalLandingUnverified = "landing-unverified"
	LandRefusalUnfencedTarget    = "target-has-no-strict-fence"
)

// LandOutcome is the envelope outcome. It maps onto the exit-code contract:
// success is 0, findings is 1, refused is 2.
type LandOutcome string

const (
	LandSuccess  LandOutcome = "success"
	LandFindings LandOutcome = "findings"
	LandRefused  LandOutcome = "refused"
)

// PullRequestLandOptions identifies one pull request to land.
type PullRequestLandOptions struct {
	Repository   string
	PullRequest  string
	ProjectsRoot string
	// Keep retains the task's worktrees and claims. Cleanup is the default
	// precisely because the opt-in form was never passed.
	Keep bool
	// ApprovedBy records the review that authorized a non-mechanical change: a
	// review file path or a pull-request comment URL. The durable review ledger
	// is a later phase; until it exists this value is recorded verbatim on the
	// receipt so the approval is at least attributable.
	ApprovedBy string
	// MergeMethod is squash by default: one reviewed change, one commit.
	MergeMethod string
	// Subject overrides the squash commit subject. The default is the pull
	// request's own title, which is the thing GitHub will otherwise replace
	// with the branch's first commit subject.
	Subject string
	// KeepCommits names source commits that must land as their own commits
	// instead of being folded into the aggregate. Reason is mandatory with it:
	// the exception has to be justified in the history it creates.
	KeepCommits []string
	Reason      string
	// BuildCommand overrides the per-kept-commit build guard. Empty uses the
	// repository's own target, which is `go build ./...` for a Go module.
	BuildCommand []string
	// AllowUnfenced lands on observed checks alone, where the target branch has
	// no server-enforced strict up-to-date policy. Without such a fence, green
	// checks prove the head was green, not that it is still green against the
	// target the merge will use, so this is an explicit widening rather than a
	// default — and the receipt records that it was used.
	AllowUnfenced bool
	// Slice is the total foreground wait budget retained under its historical
	// name for API compatibility. A landing may outlive the bounded CI waiter:
	// WB divides this budget into exact-identity observation slices instead of
	// rejecting an otherwise valid long-running landing.
	Slice             time.Duration
	CheckPollInterval time.Duration
	Progress          func(PullRequestWaitProgress)
	OperationProgress progress.Reporter
	// Events receives one structured record per invocation, whatever the
	// outcome. A refusal is the most useful event of all — it is the one that
	// says a verb was reached and declined — so `--keep` and every refusal
	// write one too. A nil appender discards.
	Events streams.EventAppender
	// Stream names the stream this landing belongs to, when it belongs to one.
	Stream string
	Now    func() time.Time
	// mergeAttempted is a test seam recording that the merge write was issued.
	beforeMerge func()
}

// PullRequestLandResult is the receipt, and the JSON envelope.
type PullRequestLandResult struct {
	SchemaVersion int         `json:"v"`
	Verb          string      `json:"verb"`
	Outcome       LandOutcome `json:"outcome"`
	RefusalCode   string      `json:"refusal_code,omitempty"`
	// SanctionedCommand is the exact command that satisfies the guard that
	// fired. A refusal an agent cannot resolve becomes a hand-written
	// workaround, which is how the cleanup path was bypassed in the first place.
	SanctionedCommand string `json:"sanctioned_command,omitempty"`
	Reason            string `json:"reason,omitempty"`

	Repository  string `json:"repository"`
	PullRequest int    `json:"pull_request"`
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	HeadRef     string `json:"head_ref,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
	BaseRef     string `json:"base_ref,omitempty"`
	MergeSHA    string `json:"merge_sha,omitempty"`
	Subject     string `json:"subject,omitempty"`

	// Mechanical records the diff-derived classification and the files it was
	// derived from, so a reader can check the judgement rather than trust it.
	Mechanical   bool     `json:"mechanical"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	NonManifest  []string `json:"non_manifest_files,omitempty"`
	ApprovedBy   string   `json:"approved_by,omitempty"`

	Checks *PullRequestWaitResult `json:"checks,omitempty"`

	BranchDeleted bool `json:"branch_deleted"`
	LandingOnBase bool `json:"landing_on_base"`
	// Commits pairs every source commit with the commit that landed it, and
	// marks the ones kept separate. GitHub's rebase merge rewrites the SHAs, so
	// after landing this pairing is the only way back to the originals.
	Commits        []LandedCommit `json:"commits,omitempty"`
	KeptCommits    []string       `json:"kept_commits,omitempty"`
	KeepReason     string         `json:"keep_reason,omitempty"`
	CleanedTasks   []string       `json:"cleaned_tasks,omitempty"`
	CleanupReports []string       `json:"cleanup_reports,omitempty"`
	Kept           bool           `json:"kept"`

	// ManualEquivalent is the ordered list of calls a caller would otherwise
	// have made. SavedToolCalls is that count minus one — the one call they
	// made instead.
	ManualEquivalent    []string `json:"manual_equivalent"`
	SavedToolCalls      int      `json:"saved_tool_calls"`
	SavedTokensEstimate int      `json:"saved_tokens_est"`
	// AbsorbedPolls counts the check observations the verb waited through.
	// Absorbing a poll loop is the largest single saving it makes.
	AbsorbedPolls int `json:"absorbed_polls"`

	Evidence map[string]string `json:"evidence,omitempty"`
}

// ExitCode maps the outcome onto WB's exit contract.
func (result PullRequestLandResult) ExitCode() int {
	switch result.Outcome {
	case LandSuccess:
		return 0
	case LandRefused:
		return 2
	default:
		return 1
	}
}

// perCallTokenOverhead is the estimated prompt-and-response cost of one tool
// call an agent does not have to make. It is deliberately conservative and is
// always displayed as an estimate; a calibration pass against harness truth is
// a later phase.
const perCallTokenOverhead = 400

// LandPullRequest verifies, merges, and tidies up after one pull request.
func LandPullRequest(ctx context.Context, options PullRequestLandOptions) (result PullRequestLandResult, err error) {
	started := time.Now()
	defer func() {
		// Every outcome leaves exactly one event, including the error paths:
		// a verb that only records its successes produces a log in which
		// nothing ever goes wrong.
		appendLandEvent(options, result, started, err)
	}()
	return landPullRequest(ctx, options)
}

func landPullRequest(ctx context.Context, options PullRequestLandOptions) (PullRequestLandResult, error) {
	number, err := PullRequestNumber(options.PullRequest)
	if err != nil {
		return PullRequestLandResult{}, err
	}
	if strings.TrimSpace(options.Repository) == "" {
		return PullRequestLandResult{}, fmt.Errorf("repository is required (owner/repository#number)")
	}
	if options.MergeMethod == "" {
		options.MergeMethod = "squash"
	}
	switch options.MergeMethod {
	case "squash", "merge", "rebase":
	default:
		return PullRequestLandResult{}, fmt.Errorf("unsupported merge method %q; use squash, merge, or rebase", options.MergeMethod)
	}
	result := PullRequestLandResult{
		SchemaVersion: 1,
		Verb:          "pr land",
		Repository:    options.Repository,
		Kept:          options.Keep,
		Evidence:      map[string]string{},
		ManualEquivalent: []string{
			"gh pr view " + number + " --repo " + options.Repository,
			"gh api repos/" + options.Repository + "/pulls/" + number + "/files",
			"gh pr checks " + number + " --repo " + options.Repository + "  (repeated until settled)",
			"gh pr merge " + number + " --repo " + options.Repository + " --squash --subject …",
			"gh api repos/" + options.Repository + "/pulls/" + number + "  (verify merged)",
			"gh api repos/" + options.Repository + "/compare/…  (verify the merge is on the base)",
			"gh api --method DELETE repos/" + options.Repository + "/git/refs/heads/…",
			"wb worktree cleanup <task> --apply",
		},
	}

	// Re-read the pull request now. A value read at session start is a
	// snapshot, and everything below is decided against the live one.
	reportPullRequestLandProgress(options.OperationProgress, "inspect_pull_request", progress.Started, options.Repository+"#"+number, 0, 0)
	view, err := ReadPullRequest(ctx, options.Repository, number)
	if err != nil {
		return result, err
	}
	reportPullRequestLandProgress(options.OperationProgress, "inspect_pull_request", progress.Completed, shortMergeRevision(view.Head.SHA), 0, 0)
	result.PullRequest = view.Number
	result.URL = view.HTMLURL
	result.Title = view.Title
	result.HeadRef = view.Head.Ref
	result.HeadSHA = view.Head.SHA
	result.BaseRef = view.Base.Ref
	result.Evidence["head"] = shortMergeRevision(view.Head.SHA)
	result.Evidence["base"] = view.Base.Ref
	result.Evidence["mergeable_state"] = view.MergeableState

	if refusal := landPreflightRefusal(view, options.Repository, number); refusal != nil {
		return mergeRefusal(result, *refusal), nil
	}
	if len(options.KeepCommits) > 0 && !options.AllowUnfenced && !targetHasRequiredChecks(ctx, options.Repository, view.Base.Ref) {
		// Rewriting the branch means the checks that were observed no longer
		// describe what will land. Without a server-enforced required check on
		// the target, nothing will re-observe the rewritten head either, so the
		// landing would be authorized by a receipt for content that no longer
		// exists.
		return mergeRefusal(result, landRefusal{
			code: LandRefusalUnfencedTarget,
			reason: "keeping commits separate rewrites the branch, and " + view.Base.Ref +
				" has no required status check to re-observe the rewritten head against",
			command: "wb pr land " + options.Repository + "#" + number + " --keep-commits " +
				strings.Join(options.KeepCommits, ",") + " --reason \"…\" --allow-unfenced",
		}), nil
	}
	if len(options.KeepCommits) > 0 && strings.TrimSpace(options.Reason) == "" {
		return mergeRefusal(result, landRefusal{
			code: LandRefusalKeepReasonMissing,
			reason: "keeping commits separate is an exception to the aggregated squash, and the exception " +
				"has to be justified in the history it creates",
			command: "wb pr land " + options.Repository + "#" + number +
				" --keep-commits " + strings.Join(options.KeepCommits, ",") + " --reason \"<why these commits stand alone>\"",
		}), nil
	}

	reportPullRequestLandProgress(options.OperationProgress, "inspect_changed_files", progress.Started, options.Repository+"#"+number, 0, 0)
	files, err := pullRequestChangedFiles(ctx, options.Repository, number)
	if err != nil {
		return result, err
	}
	reportPullRequestLandProgress(options.OperationProgress, "inspect_changed_files", progress.Completed, "files", len(files), len(files))
	for _, file := range files {
		result.ChangedFiles = append(result.ChangedFiles, file.Filename)
	}
	verdict := ClassifyMechanical(files)
	result.Mechanical, result.NonManifest = verdict.Mechanical, verdict.NonManifest
	result.Evidence["classification"] = "from-diff-content"
	if !verdict.Mechanical {
		result.Evidence["not_mechanical_because"] = verdict.Summary()
	}

	result.ApprovedBy = strings.TrimSpace(options.ApprovedBy)
	if !result.Mechanical && result.ApprovedBy == "" {
		return mergeRefusal(result, landRefusal{
			code: LandRefusalUnapprovedPatch,
			reason: "this change is not a mechanical dependency bump (" + verdict.Summary() +
				"), so it needs a recorded review approval before it can land",
			command: "wb pr land " + options.Repository + "#" + number + " --approved-by <review-file-or-comment-url>",
		}), nil
	}

	// Wait for the checks the target's own policy requires, on this exact head.
	waitOptions := PullRequestWaitOptions{
		Repository:        options.Repository,
		PullRequest:       number,
		Target:            view.Base.Ref,
		Head:              view.Head.SHA,
		AllowUnfenced:     options.AllowUnfenced,
		Slice:             options.Slice,
		CheckPollInterval: options.CheckPollInterval,
		Progress:          options.Progress,
		OperationProgress: options.OperationProgress,
	}
	reportPullRequestLandProgress(options.OperationProgress, "candidate_checks", progress.Waiting, shortMergeRevision(view.Head.SHA), 0, 0)
	waited, err := waitForPullRequestLandChecks(ctx, waitOptions)
	if err != nil {
		return result, err
	}
	reportPullRequestLandProgress(options.OperationProgress, "candidate_checks", progress.Completed, string(waited.Status), len(waited.Checks), len(waited.Checks))
	result.Checks = &waited
	result.AbsorbedPolls = waited.StableObservations
	switch waited.Status {
	case PullRequestWaitPassed:
	case PullRequestWaitPending:
		result.Outcome = LandFindings
		result.RefusalCode = LandRefusalChecksPending
		result.Reason = waited.Reason
		result.SanctionedCommand = "wb pr land " + options.Repository + "#" + number
		return withSavings(result), nil
	default:
		result.Outcome = LandFindings
		result.RefusalCode = LandRefusalChecksFailed
		result.Reason = waited.Reason
		result.SanctionedCommand = "gh pr view " + number + " --repo " + options.Repository + " --web"
		if strings.Contains(waited.Reason, "strict up-to-date fence") {
			// This is a policy gap, not a red check: say which, and name the
			// widening rather than leaving the operator to guess that green
			// checks were not the problem.
			result.RefusalCode = LandRefusalUnfencedTarget
			result.SanctionedCommand = "wb pr land " + options.Repository + "#" + number + " --allow-unfenced"
		}
		return withSavings(result), nil
	}
	if options.AllowUnfenced {
		result.Evidence["fence"] = "none; landed on observed checks under --allow-unfenced"
		result.Evidence["allow_unfenced"] = "true"
		if waited.PolicyAuthorityUnavailable != "" {
			result.Evidence["required_check_policy"] = "unavailable: " + waited.PolicyAuthorityUnavailable
		}
	}

	// Pre-flight the cleanup now, while refusing is still free. Discovering
	// after the merge that the worktree cannot be retired leaves the landing
	// done and the tidy-up impossible, which is the shape that produced sixty
	// abandoned checkouts in the first place.
	// The live-link half of this runs whatever --keep says. --keep opts out of
	// retiring the worktree; it does not opt out of the rule that a worktree
	// building against an unpublished tree must not be landed, and reading it
	// as a bypass would make the guard optional by accident.
	reportPullRequestLandProgress(options.OperationProgress, "preflight_cleanup", progress.Started, view.Head.Ref, 0, 0)
	if refusal := preflightLandingCleanup(ctx, options, view, number, options.Keep); refusal != nil {
		return mergeRefusal(result, *refusal), nil
	}
	reportPullRequestLandProgress(options.OperationProgress, "preflight_cleanup", progress.Completed, view.Head.Ref, 0, 0)

	subject := strings.TrimSpace(options.Subject)
	if subject == "" {
		// GitHub takes the branch's first commit subject when none is given, so
		// a `wip(...)` or `fix typo` message lands on the default branch
		// verbatim and cannot be corrected without rewriting history.
		subject = fmt.Sprintf("%s (#%d)", view.Title, view.Number)
	}
	result.Subject = subject

	reportPullRequestLandProgress(options.OperationProgress, "inspect_source_commits", progress.Started, view.Head.Ref, 0, 0)
	sourceCommits, err := pullRequestCommits(ctx, options.Repository, number)
	if err != nil {
		return result, err
	}
	reportPullRequestLandProgress(options.OperationProgress, "inspect_source_commits", progress.Completed, "commits", len(sourceCommits), len(sourceCommits))
	body := aggregatedCommitMessage(view, sourceCommits, result.ApprovedBy, options.Reason)
	head := view.Head.SHA
	mergeMethod := options.MergeMethod

	if len(options.KeepCommits) > 0 {
		kept, keptHead, refusal, keepErr := landKeepingCommits(ctx, options, view, sourceCommits, number, result.ApprovedBy)
		if keepErr != nil {
			return result, keepErr
		}
		if refusal != nil {
			return mergeRefusal(result, *refusal), nil
		}
		result.Commits = kept
		result.KeepReason = strings.TrimSpace(options.Reason)
		for _, commit := range kept {
			if commit.Kept {
				result.KeptCommits = append(result.KeptCommits, commit.SourceSHA)
			}
		}
		head = keptHead
		// The branch now holds exactly the commits that should appear on the
		// base, so the landing route is the one that replays them individually.
		mergeMethod = "rebase"
		result.HeadSHA = keptHead
		result.Evidence["rewritten_head"] = shortMergeRevision(keptHead)

		// The checks observed above were the OLD head's. The rewritten branch
		// is different content in a different order, and merging it on the
		// strength of a receipt for something else is exactly the substitution
		// the head-SHA lease exists to prevent. Wait for its own.
		rewritten := waitOptions
		rewritten.Head = keptHead
		reobserved, waitErr := waitForPullRequestLandChecks(ctx, rewritten)
		if waitErr != nil {
			return result, waitErr
		}
		result.Checks = &reobserved
		result.AbsorbedPolls += reobserved.StableObservations
		if reobserved.Status != PullRequestWaitPassed {
			result.Outcome = LandFindings
			result.RefusalCode = LandRefusalChecksPending
			if reobserved.Status == PullRequestWaitFailed {
				result.RefusalCode = LandRefusalChecksFailed
			}
			result.Reason = "the rewritten branch's own checks are not green: " + reobserved.Reason
			result.SanctionedCommand = "wb pr land " + options.Repository + "#" + number +
				" --keep-commits " + strings.Join(options.KeepCommits, ",") + " --reason " + strconv.Quote(options.Reason)
			return withSavings(result), nil
		}
	}

	if options.beforeMerge != nil {
		options.beforeMerge()
	}
	reportPullRequestLandProgress(options.OperationProgress, "merge_pull_request", progress.Started, shortMergeRevision(head), 0, 0)
	merge, refusal, err := mergePullRequest(ctx, options.Repository, number, head, mergeMethod, subject, body)
	if err != nil {
		return result, err
	}
	if refusal != nil {
		return mergeRefusal(result, *refusal), nil
	}
	result.MergeSHA = merge
	reportPullRequestLandProgress(options.OperationProgress, "merge_pull_request", progress.Completed, shortMergeRevision(merge), 0, 0)

	// Assert the observable effect rather than the exit status of the call that
	// was supposed to produce it.
	reportPullRequestLandProgress(options.OperationProgress, "verify_remote_landing", progress.Started, view.Base.Ref, 0, 0)
	landed, err := ReadPullRequest(ctx, options.Repository, number)
	if err != nil {
		return result, err
	}
	if !landed.Merged || landed.MergeCommitSHA == "" {
		result.Outcome = LandFindings
		result.RefusalCode = LandRefusalLandingUnverified
		result.Reason = "GitHub accepted the merge but the pull request does not report itself merged"
		result.SanctionedCommand = "wb pr land " + options.Repository + "#" + number
		return withSavings(result), nil
	}
	result.MergeSHA = landed.MergeCommitSHA
	result.Evidence["merge_commit"] = shortMergeRevision(landed.MergeCommitSHA)
	if len(result.Commits) > 0 {
		// The landed SHAs exist only now: a rebase merge replays every commit.
		canonical, _, _, locateErr := locateBranchCheckout(ctx, options.ProjectsRoot, options.Repository, view.Head.Ref, view.Base.Ref)
		if locateErr == nil && canonical != "" {
			if _, fetchErr := runGit(ctx, canonical, "fetch", "origin", view.Base.Ref); fetchErr == nil {
				mapped, mapErr := MapLandedCommits(ctx, canonical, "refs/remotes/origin/"+view.Base.Ref, view.Base.SHA, result.Commits)
				if mapErr == nil {
					result.Commits = mapped
				}
			}
		}
	}

	onBase, err := commitIsOnBranch(ctx, options.Repository, landed.MergeCommitSHA, view.Base.Ref)
	if err != nil {
		return result, err
	}
	result.LandingOnBase = onBase
	if !onBase {
		result.Outcome = LandFindings
		result.RefusalCode = LandRefusalLandingUnverified
		result.Reason = "merge commit " + shortMergeRevision(landed.MergeCommitSHA) + " is not reachable from " + view.Base.Ref
		result.SanctionedCommand = "wb pr land " + options.Repository + "#" + number
		return withSavings(result), nil
	}
	reportPullRequestLandProgress(options.OperationProgress, "verify_remote_landing", progress.Completed, shortMergeRevision(landed.MergeCommitSHA), 0, 0)

	reportPullRequestLandProgress(options.OperationProgress, "delete_remote_branch", progress.Started, view.Head.Ref, 0, 0)
	if deleted, deleteErr := deleteRemoteBranch(ctx, options.Repository, view, landed); deleteErr != nil {
		return result, deleteErr
	} else {
		result.BranchDeleted = deleted
	}
	reportPullRequestLandProgress(options.OperationProgress, "delete_remote_branch", progress.Completed, view.Head.Ref, 0, 0)

	if !options.Keep {
		reportPullRequestLandProgress(options.OperationProgress, "cleanup", progress.Started, view.Head.Ref, 0, 0)
		tasks, reports, cleanupErr := cleanupLandedWorktrees(ctx, options.ProjectsRoot, options.Repository, view.Head.Ref, view.Base.Ref, landed.MergeCommitSHA)
		result.CleanedTasks = tasks
		result.CleanupReports = reports
		if cleanupErr == nil && len(tasks) == 0 {
			// Silence here reads as "the worktree was retired". Say instead
			// that there was none, so a caller who expected one knows to look.
			result.Evidence["cleanup"] = "no WB worktree for " + options.Repository + " on " + view.Head.Ref +
				"; nothing to retire"
		}
		if cleanupErr != nil {
			// The landing itself succeeded and must be reported as such; a
			// checkout that could not be retired is a finding, with the verb
			// that finishes it named.
			result.Outcome = LandFindings
			result.RefusalCode = "cleanup-incomplete"
			result.Reason = cleanupErr.Error()
			result.SanctionedCommand = "wb worktree gc --apply"
			return withSavings(result), nil
		}
		reportPullRequestLandProgress(options.OperationProgress, "cleanup", progress.Completed, "tasks", len(tasks), len(tasks))
	}

	result.Outcome = LandSuccess
	return withSavings(result), nil
}

// pullRequestLandWaitSlice selects the next bounded slice from a user-facing
// landing timeout. Returning one slice at a time avoids allocating from an
// attacker-controlled duration while preserving the full final remainder.
func pullRequestLandWaitSlice(remaining time.Duration) (time.Duration, error) {
	if remaining <= 0 {
		return 0, fmt.Errorf("pull request landing timeout must be positive")
	}
	return min(remaining, MaxForegroundCheckWaitSlice), nil
}

// waitForPullRequestLandChecks keeps one CLI call alive for its requested
// total budget while every individual observation remains resumable and below
// the harness-safe ceiling. Each slice reuses the same repository, PR, target,
// and exact head; any drift is therefore still refused by the underlying
// observer.
func waitForPullRequestLandChecks(ctx context.Context, options PullRequestWaitOptions) (PullRequestWaitResult, error) {
	return waitForPullRequestLandChecksWith(ctx, options, WaitForPullRequestChecks)
}

func waitForPullRequestLandChecksWith(
	ctx context.Context,
	options PullRequestWaitOptions,
	wait func(context.Context, PullRequestWaitOptions) (PullRequestWaitResult, error),
) (PullRequestWaitResult, error) {
	if options.Slice <= 0 {
		return PullRequestWaitResult{}, fmt.Errorf("pull request landing timeout must be positive")
	}
	var waited PullRequestWaitResult
	for remaining := options.Slice; remaining > 0; {
		slice, err := pullRequestLandWaitSlice(remaining)
		if err != nil {
			return PullRequestWaitResult{}, err
		}
		current := options
		current.Slice = slice
		if current.CheckPollInterval >= slice {
			return PullRequestWaitResult{}, fmt.Errorf("check poll interval must be shorter than the total foreground timeout")
		}
		waited, err = wait(ctx, current)
		if err != nil || waited.Status != PullRequestWaitPending {
			return waited, err
		}
		remaining -= slice
	}
	return waited, nil
}

func reportPullRequestLandProgress(reporter progress.Reporter, phase string, state progress.State, detail string, completed, total int) {
	progress.Report(reporter, progress.Event{
		Operation: "pr_land", Phase: phase, State: state, Detail: detail,
		Completed: completed, Total: total,
	})
}

type landRefusal struct {
	code    string
	reason  string
	command string
}

func mergeRefusal(result PullRequestLandResult, refusal landRefusal) PullRequestLandResult {
	result.Outcome = LandRefused
	result.RefusalCode = refusal.code
	result.Reason = refusal.reason
	result.SanctionedCommand = refusal.command
	return withSavings(result)
}

func landPreflightRefusal(view PullRequestView, repository, number string) *landRefusal {
	switch {
	case view.Merged:
		return &landRefusal{
			code:    LandRefusalNotOpen,
			reason:  "pull request is already merged as " + shortMergeRevision(view.MergeCommitSHA),
			command: "wb worktree gc --apply",
		}
	case !strings.EqualFold(view.State, "open"):
		return &landRefusal{
			code:    LandRefusalNotOpen,
			reason:  "pull request state is " + view.State,
			command: "gh pr view " + number + " --repo " + repository + " --web",
		}
	case view.Draft:
		return &landRefusal{
			code:    LandRefusalDraft,
			reason:  "pull request is a draft; landing one would bypass the review it is waiting for",
			command: "gh pr ready " + number + " --repo " + repository,
		}
	case view.Locked:
		return &landRefusal{
			code:    LandRefusalLocked,
			reason:  "pull request conversation is locked",
			command: "gh pr view " + number + " --repo " + repository + " --web",
		}
	case view.Mergeable != nil && !*view.Mergeable:
		return &landRefusal{
			code:    LandRefusalNotMergeable,
			reason:  "GitHub reports the pull request as not mergeable (" + view.MergeableState + ")",
			command: "wb worktree merge " + repository + " --route auto",
		}
	}
	return nil
}

func pullRequestChangedFiles(ctx context.Context, repository, number string) ([]ChangedFile, error) {
	responses, err := githubobserver.GetPages(ctx, githubobserver.GetRequest{
		Repository: repository,
		Endpoint:   "repos/" + repository + "/pulls/" + url.PathEscape(number) + "/files?per_page=100",
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("read changed files for %s#%s: %w", repository, number, err)
	}
	seen := map[string]bool{}
	files := make([]ChangedFile, 0, 16)
	for _, response := range responses {
		var page []ChangedFile
		if err := json.Unmarshal(response.Body, &page); err != nil {
			return nil, fmt.Errorf("decode changed files for %s#%s: %w", repository, number, err)
		}
		for _, file := range page {
			name := strings.TrimSpace(file.Filename)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			file.Filename = name
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Filename < files[j].Filename })
	return files, nil
}

type mergeResponse struct {
	SHA     string `json:"sha"`
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

// mergePullRequest issues the merge with the head SHA as a lease: GitHub
// refuses the call outright if the branch moved since the checks were observed,
// so a race cannot land an unverified head.
func mergePullRequest(ctx context.Context, repository, number, head, method, subject, body string) (string, *landRefusal, error) {
	arguments := []string{"api", "--method", "PUT",
		"repos/" + repository + "/pulls/" + url.PathEscape(number) + "/merge",
		"-f", "merge_method=" + method,
		"-f", "sha=" + head,
	}
	if method == "squash" {
		// Only a squash produces a commit message of WB's own; a rebase merge
		// replays the branch's commits with their own messages, which is the
		// whole point of having kept them.
		arguments = append(arguments, "-f", "commit_title="+subject, "-f", "commit_message="+body)
	}
	response := githubExecute(ctx, "", arguments...)
	var decoded mergeResponse
	_ = json.Unmarshal(response.Stdout, &decoded)
	if response.Err != nil || !decoded.Merged {
		message := strings.TrimSpace(decoded.Message)
		if message == "" {
			message = strings.TrimSpace(string(response.Stderr))
		}
		if message == "" {
			message = strings.TrimSpace(string(response.Stdout))
		}
		if strings.Contains(strings.ToLower(message), "head branch was modified") ||
			strings.Contains(strings.ToLower(message), "base branch was modified") {
			return "", &landRefusal{
				code:    LandRefusalHeadMoved,
				reason:  "the branch moved after its checks were observed: " + message,
				command: "wb pr land " + repository + "#" + number,
			}, nil
		}
		return "", &landRefusal{
			code:    LandRefusalMergeRejected,
			reason:  "GitHub refused the merge: " + message,
			command: "gh pr view " + number + " --repo " + repository + " --web",
		}, nil
	}
	return decoded.SHA, nil, nil
}

type compareResponse struct {
	Status string `json:"status"`
}

// commitIsOnBranch proves the merge reached the base, rather than trusting the
// call that was supposed to put it there.
func commitIsOnBranch(ctx context.Context, repository, commit, branch string) (bool, error) {
	body, err := githubGet(ctx, "", repository, branch, commit,
		"repos/"+repository+"/compare/"+url.PathEscape(commit)+"..."+url.PathEscape(branch))
	if err != nil {
		return false, fmt.Errorf("compare %s with %s: %w", shortMergeRevision(commit), branch, err)
	}
	var compare compareResponse
	if err := json.Unmarshal(body, &compare); err != nil {
		return false, fmt.Errorf("decode comparison of %s with %s: %w", shortMergeRevision(commit), branch, err)
	}
	// identical: the base is exactly this commit. ahead: the base has moved on
	// past it. Either way the base contains it; "behind" or "diverged" mean it
	// does not.
	return compare.Status == "identical" || compare.Status == "ahead", nil
}

// deleteRemoteBranch retires the source branch and verifies it is gone. It
// never touches a branch in another repository — a fork's head is not this
// repository's to delete — and treats an already-absent ref as success,
// because GitHub's own "automatically delete head branches" setting may have
// removed it first.
func deleteRemoteBranch(ctx context.Context, repository string, view, landed PullRequestView) (bool, error) {
	if view.Head.Repo == nil || !strings.EqualFold(view.Head.Repo.FullName, repository) {
		return false, nil
	}
	ref := strings.TrimSpace(view.Head.Ref)
	if ref == "" || strings.EqualFold(ref, landed.Base.Ref) {
		return false, nil
	}
	response := githubExecute(ctx, "", "api", "--method", "DELETE",
		"repos/"+repository+"/git/refs/heads/"+ref)
	if response.Err != nil && !branchAlreadyGone(response.Stdout, response.Stderr) {
		return false, fmt.Errorf("delete branch %s: %s", ref,
			strings.TrimSpace(string(response.Stderr)+string(response.Stdout)))
	}
	// Verify the effect: ask for the ref and require it to be absent.
	check := githubExecute(ctx, "", "api", "repos/"+repository+"/git/ref/heads/"+ref)
	if check.Err == nil {
		return false, fmt.Errorf("branch %s still exists on origin after its deletion was accepted", ref)
	}
	return true, nil
}

func branchAlreadyGone(stdout, stderr []byte) bool {
	body := strings.ToLower(strings.TrimSpace(string(stdout) + " " + string(stderr)))
	return strings.Contains(body, "reference does not exist") || strings.Contains(body, "not found")
}

// cleanupLandedWorktrees retires every WB worktree that produced this branch.
// It reuses the ordinary cleanup transaction — one deletion path, one durable
// Work Log seal — and passes the landing commit as the absorbed-by receipt,
// which is what makes a squash landing provable.
func cleanupLandedWorktrees(ctx context.Context, projectsRoot, repository, headRef, base, landingSHA string) ([]string, []string, error) {
	listed, err := worktrees.ListWithDiagnostics(ctx, worktrees.ListOptions{
		ProjectsRoot: projectsRoot,
		Base:         base,
		Filter:       repository,
	})
	if err != nil {
		return nil, nil, err
	}
	tasks := make([]string, 0, 1)
	seen := map[string]bool{}
	for _, entry := range listed.Results {
		if entry.Repository != repository || entry.Branch != headRef || seen[entry.Task] {
			continue
		}
		seen[entry.Task] = true
		tasks = append(tasks, entry.Task)
	}
	cleaned := make([]string, 0, len(tasks))
	reports := make([]string, 0, len(tasks))
	for _, task := range tasks {
		outcome, cleanupErr := worktrees.Cleanup(ctx, worktrees.CleanupOptions{
			ProjectsRoot:    projectsRoot,
			Tasks:           []string{task},
			ExactRepository: repository,
			Base:            base,
			AbsorbedBy:      landingSHA,
			Apply:           true,
			OlderThan:       0,
			Workers:         1,
		})
		if outcome.ReportPath != "" {
			reports = append(reports, outcome.ReportPath)
		}
		if cleanupErr != nil {
			return cleaned, reports, fmt.Errorf("retire worktree for task %s: %w", task, cleanupErr)
		}
		applied := false
		for _, entry := range outcome.Results {
			if entry.Applied {
				applied = true
			}
		}
		if !applied {
			reason := "cleanup reported no applied result"
			for _, entry := range outcome.Results {
				if entry.Reason != "" {
					reason = entry.Reason
					break
				}
			}
			return cleaned, reports, fmt.Errorf("worktree for task %s was not retired: %s", task, reason)
		}
		cleaned = append(cleaned, task)
	}
	return cleaned, reports, nil
}

// withSavings records what the caller did not have to do. The estimate is
// labelled an estimate everywhere it is displayed.
func withSavings(result PullRequestLandResult) PullRequestLandResult {
	if result.Outcome == LandRefused {
		// A refused invocation did not do the caller's work, so it saved them
		// nothing. Counting the calls it happened to make before refusing would
		// inflate every savings total with the runs that achieved nothing.
		result.SavedToolCalls = 0
		result.SavedTokensEstimate = 0
		return result
	}
	calls := len(result.ManualEquivalent)
	if result.AbsorbedPolls > 1 {
		// Each absorbed poll is a call the caller would have made; absorbing a
		// poll loop is the largest single saving this verb makes.
		calls += result.AbsorbedPolls - 1
	}
	if result.Kept {
		calls--
	}
	if calls < 1 {
		calls = 1
	}
	result.SavedToolCalls = calls - 1
	bytesAbsorbed := 0
	for _, file := range result.ChangedFiles {
		bytesAbsorbed += len(file)
	}
	if result.Checks != nil {
		for _, check := range result.Checks.Checks {
			bytesAbsorbed += len(check.Name) + len(check.Link) + len(check.Bucket)
		}
	}
	result.SavedTokensEstimate = bytesAbsorbed/4 + result.SavedToolCalls*perCallTokenOverhead
	return result
}

// FooterLine is the interactive-mode summary. It is suppressed under
// --non-interactive, where the JSON envelope carries the same figures.
func (result PullRequestLandResult) FooterLine() string {
	return fmt.Sprintf("saved %d tool calls, ~%s tokens (estimate)",
		result.SavedToolCalls, approximateTokens(result.SavedTokensEstimate))
}

func approximateTokens(tokens int) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	return fmt.Sprintf("%.1fk", float64(tokens)/1000)
}

func limitStrings(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	trimmed := append([]string(nil), values[:limit]...)
	return append(trimmed, fmt.Sprintf("and %d more", len(values)-limit))
}

// SourceCommit is one commit of the pull request's branch.
type SourceCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Body    string `json:"body,omitempty"`
}

type commitListEntry struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

// pullRequestCommits lists the branch's commits in order.
func pullRequestCommits(ctx context.Context, repository, number string) ([]SourceCommit, error) {
	responses, err := githubobserver.GetPages(ctx, githubobserver.GetRequest{
		Repository: repository,
		Endpoint:   "repos/" + repository + "/pulls/" + url.PathEscape(number) + "/commits?per_page=100",
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("read commits of %s#%s: %w", repository, number, err)
	}
	commits := make([]SourceCommit, 0, 8)
	for _, response := range responses {
		var page []commitListEntry
		if err := json.Unmarshal(response.Body, &page); err != nil {
			return nil, fmt.Errorf("decode commits of %s#%s: %w", repository, number, err)
		}
		for _, entry := range page {
			subject, body, _ := strings.Cut(strings.TrimSpace(entry.Commit.Message), "\n")
			commits = append(commits, SourceCommit{
				SHA:     entry.SHA,
				Subject: strings.TrimSpace(subject),
				Body:    strings.TrimSpace(body),
			})
		}
	}
	return commits, nil
}

// aggregatedCommitMessage builds the squash body.
//
// A squash that keeps only the pull request's title throws away every commit
// message the branch carried, and `git log` on the default branch then cannot
// answer what a change actually contained. The aggregate keeps them: one line
// per source commit, with any commit that carried a real body folded under its
// own line, plus the pull request number and the review that authorized it.
//
// The subject is deliberately the pull request title. GitHub substitutes the
// branch's first commit subject when none is given, so a `wip(...)` or
// `fix typo` message lands on the default branch verbatim — and correcting it
// afterwards means rewriting history on a protected branch, which is to say it
// cannot be corrected at all.
// repositoryOf names the repository a pull request belongs to. The head's own
// repository is authoritative for a same-repository pull request and is what a
// reader needs to find it again; a fork's head names the fork, so the caller
// supplies the base repository through the view it read.
func repositoryOf(view PullRequestView) string {
	if view.Base.Repo != nil && strings.TrimSpace(view.Base.Repo.FullName) != "" {
		return view.Base.Repo.FullName
	}
	if view.Head.Repo != nil && strings.TrimSpace(view.Head.Repo.FullName) != "" {
		return view.Head.Repo.FullName
	}
	return ""
}

func aggregatedCommitMessage(view PullRequestView, commits []SourceCommit, approvedBy, reason string) string {
	var builder strings.Builder
	if summary := pullRequestBodySummary(view.Body); summary != "" {
		builder.WriteString(summary)
		builder.WriteString("\n\n")
	}
	if len(commits) > 0 {
		builder.WriteString("Source commits:\n\n")
		for _, commit := range commits {
			builder.WriteString("- " + shortMergeRevision(commit.SHA) + " " + commit.Subject + "\n")
			for _, line := range informativeBodyLines(commit.Body) {
				builder.WriteString("  " + line + "\n")
			}
		}
		builder.WriteString("\n")
	}
	if strings.TrimSpace(reason) != "" {
		builder.WriteString("Commits kept separate because: " + strings.TrimSpace(reason) + "\n\n")
	}
	fmt.Fprintf(&builder, "Pull request: %s#%d\n", repositoryOf(view), view.Number)
	if strings.TrimSpace(approvedBy) != "" {
		builder.WriteString("Review: " + strings.TrimSpace(approvedBy) + "\n")
	} else {
		builder.WriteString("Review: mechanical dependency bump, no review ledger entry required\n")
	}
	return builder.String()
}

// pullRequestBodySummary keeps the leading prose of a pull request body and
// stops at the first heading, so the aggregate carries the summary rather than
// the whole review template.
func pullRequestBodySummary(body string) string {
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "<!--") {
			break
		}
		lines = append(lines, strings.TrimRight(line, " \t"))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// informativeBodyLines keeps a commit body only when it says something the
// subject did not. Trailers are provenance, not information about the change.
func informativeBodyLines(body string) []string {
	lines := make([]string, 0, 4)
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if isCommitTrailer(trimmed) {
			continue
		}
		lines = append(lines, trimmed)
	}
	return lines
}

func isCommitTrailer(line string) bool {
	for _, trailer := range []string{
		"Co-Authored-By:", "Co-authored-by:", "Signed-off-by:", "Claude-Session:",
		"Reviewed-by:", "Refs:", "Closes:", "Fixes:",
	} {
		if strings.HasPrefix(line, trailer) {
			return true
		}
	}
	return false
}

// targetHasRequiredChecks reports whether the target branch carries a
// server-enforced required status check. It is read from the same
// branch-protection and ruleset sources the waiter uses, so the answer is the
// one the merge will actually be judged by.
func targetHasRequiredChecks(ctx context.Context, repository, target string) bool {
	checks, _, reason := targetBranchRequiredChecks(ctx, repository, target, false)
	return reason == "" && len(checks) > 0
}

// appendLandEvent records one invocation. It never fails the landing: an event
// that cannot be written is a lost record, and refusing a completed merge
// because of one would be a far worse outcome than the gap.
func appendLandEvent(options PullRequestLandOptions, result PullRequestLandResult, started time.Time, landErr error) {
	if options.Events == nil {
		return
	}
	outcome := string(result.Outcome)
	if landErr != nil {
		outcome = string(LandFindings)
	}
	if outcome == "" {
		outcome = string(LandFindings)
	}
	evidence := map[string]string{
		"pull_request":     options.Repository + "#" + strings.TrimSpace(options.PullRequest),
		"head":             result.HeadSHA,
		"mechanical":       strconv.FormatBool(result.Mechanical),
		"saved_tool_calls": strconv.Itoa(result.SavedToolCalls),
		"kept":             strconv.FormatBool(result.Kept),
	}
	if result.MergeSHA != "" {
		evidence["merge_commit"] = result.MergeSHA
	}
	if result.ApprovedBy != "" {
		evidence["approved_by"] = result.ApprovedBy
	}
	if len(result.KeptCommits) > 0 {
		evidence["kept_commits"] = strings.Join(result.KeptCommits, ",")
	}
	if len(result.CleanedTasks) > 0 {
		evidence["cleaned_tasks"] = strings.Join(result.CleanedTasks, ",")
	}
	detail := result.Reason
	if landErr != nil {
		detail = landErr.Error()
	}
	_ = options.Events.Append(streams.Event{
		Stream:      options.Stream,
		Verb:        "pr land",
		Repository:  options.Repository,
		Outcome:     outcome,
		RefusalCode: result.RefusalCode,
		Detail:      detail,
		DurationMS:  time.Since(started).Milliseconds(),
		Evidence:    evidence,
	})
}

// preflightLandingCleanup refuses a landing whose tidy-up would fail, before
// the merge makes the landing irreversible.
//
// linksOnly is set when the caller passed --keep: the dirty-worktree check is
// about retiring a checkout and does not apply, while the live-link check is
// about what is being landed and always does.
func preflightLandingCleanup(ctx context.Context, options PullRequestLandOptions, view PullRequestView, number string, linksOnly bool) *landRefusal {
	listed, err := worktrees.ListWithDiagnostics(ctx, worktrees.ListOptions{
		ProjectsRoot: options.ProjectsRoot,
		Base:         view.Base.Ref,
		Filter:       options.Repository,
	})
	if err != nil {
		// The inventory is unreadable, which is not the same as clean. Refuse
		// rather than merge into an unknown tidy-up.
		return &landRefusal{
			code:    "cleanup-unverifiable",
			reason:  "the worktree inventory could not be read, so this landing's cleanup cannot be pre-flighted: " + err.Error(),
			command: "wb pr land " + options.Repository + "#" + number + " --keep",
		}
	}
	for _, entry := range listed.Results {
		if entry.Repository != options.Repository || entry.Branch != view.Head.Ref {
			continue
		}
		if !entry.Clean && !linksOnly {
			return &landRefusal{
				code: "cleanup-blocked-dirty",
				reason: "the worktree for task " + entry.Task + " has uncommitted changes, so landing now would " +
					"merge the work and then be unable to retire the checkout that produced it",
				command: "wb worktree end " + entry.Task + ", or land with --keep",
			}
		}
		if refusal := refuseLinkedWorktree(options.ProjectsRoot, entry); refusal != nil {
			return refusal
		}
	}
	return nil
}

// refuseLinkedWorktree refuses a checkout that still holds a live local
// dependency link, through the one implementation WB has of that question.
//
// It consults both signals — recorded stream links and a `go.work` nobody
// recorded — because either alone misses the other, and it reuses
// locallink.HasLiveLink rather than asking half the question a second way.
func refuseLinkedWorktree(projectsRoot string, entry worktrees.ListResult) *landRefusal {
	store, err := streams.Open(projectsRoot)
	if err != nil {
		// No stream state at all is the ordinary case outside a stream, but a
		// `go.work` can still exist, so the check continues with no store.
		store = nil
	}
	links, err := locallink.HasLiveLink(store, entry.WorktreeDir)
	if err != nil || len(links) == 0 {
		return nil
	}
	return &landRefusal{
		code:    "cleanup-blocked-live-link",
		reason:  locallink.RefusalMessage(entry.WorktreeDir, links),
		command: "wb deps propagate local --undo, or land with --keep",
	}
}
