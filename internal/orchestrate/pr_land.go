package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/landinglane"
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
	LandRefusalCanonicalSync     = "canonical-sync-blocked"
	// LandRefusalLandingLaneHeld reports that a different live WB session
	// already owns the (repository, target) landing lane. See
	// internal/landinglane and LaneGuardRequest.
	LandRefusalLandingLaneHeld = "landing-lane-held"
	// LandRefusalReviewCommentEmpty reports that the identity form of
	// --approved-by was given with no --review-comment/--review-comment-file
	// text, or an empty one (#604).
	LandRefusalReviewCommentEmpty = "review-comment-empty"
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
	// ApprovedBy records the review that authorized a non-mechanical change.
	// It is one of: an existing file path or a pull-request comment URL
	// (unchanged, back-compatible); the literal "ci"; or a reviewer identity
	// `{model}[@{harness}[@{session}]]`, which requires ReviewComment or
	// ReviewCommentFile and causes WB to post the review as a PR comment and
	// bind it to the exact head it reviewed (#604, #586).
	ApprovedBy string
	// ReviewComment is the review text for the identity form of ApprovedBy.
	// Mutually exclusive with ReviewCommentFile; the caller validates that.
	ReviewComment string
	// ReviewCommentFile is a path to the review text for the identity form
	// of ApprovedBy.
	ReviewCommentFile string
	// MergeMethod is merge by default: preserve commits and the reviewed PR boundary.
	MergeMethod string
	// MergeMethodExplicit distinguishes an operator-selected method from the merge
	// default. KeepCommits requires an explicitly selected squash hybrid.
	MergeMethodExplicit bool
	// Subject overrides the explicit squash commit subject. The default is the pull
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
	// NoUpdateBranch keeps the historical behaviour of refusing a candidate
	// that is behind the target instead of bringing it up to date. The default
	// updates, because a target with a strict up-to-date policy puts every
	// candidate behind whenever anything else lands, and refusing then costs a
	// manual merge plus a full fresh CI cycle.
	NoUpdateBranch bool
	// NoAutoMerge keeps the historical behaviour of returning checks-pending
	// when the wait budget runs out. The default arms GitHub auto-merge
	// instead, so a complete change is not left stranded on whoever remembers
	// it next.
	//
	// The arming is deliberately not withdrawn when checks fail. CI is the
	// gate: whoever pushes a fix is responsible for it, the required checks
	// re-run against exactly what they pushed, and the merge happens only if
	// those pass. A review that must not be skippable belongs in the workflow,
	// where it runs on every push and nobody can decline to re-request it —
	// not in an approval recorded once against a head that no longer exists.
	NoAutoMerge bool
	// Lane optionally names the acquiring session for the landing-lane
	// ownership guard (see LaneGuardRequest in internal/orchestrate). Left
	// zero, no guard runs — existing direct callers are unaffected.
	Lane            LaneGuardRequest
	CheckoutUpdated func(context.Context, CheckoutUpdate)
	// mergeAttempted is a test seam recording that the merge write was issued.
	beforeMerge func()
	// headUpdated is called by the shared awaitLandablePullRequest engine
	// after every successful update-branch and re-read, with the previous
	// and the updated head SHA. A non-nil error aborts the wait and is
	// returned to the caller unchanged. `wb pr land` leaves it nil; the
	// worktree-merge PR route uses it to persist the receipt's advanced
	// target/candidate before fast-forwarding the local candidate worktree.
	headUpdated func(previous, updated string) error
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
	// AutoMergeArmed records that this invocation armed GitHub auto-merge. It
	// says nothing about who performed the merge: evidence "merged_by" is set
	// when GitHub did. When the invocation ends before the merge, GitHub lands
	// the change without WB, and the worktree is still the caller's to retire.
	AutoMergeArmed bool   `json:"auto_merge_armed,omitempty"`
	Reason         string `json:"reason,omitempty"`

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
	// Reviewer, ReviewedHeadSHA, ReviewDigest, ReviewCommentURL, and
	// SelfReview are the #604/#586 receipt fields: the full reviewer
	// identity triple, the exact head it reviewed, a digest of the review
	// text, the posted comment's URL (identity form only), and whether the
	// reviewer identity fully matches this session's own (see
	// currentSessionIdentity).
	Reviewer         string `json:"reviewer,omitempty"`
	ReviewedHeadSHA  string `json:"reviewed_head,omitempty"`
	ReviewDigest     string `json:"review_digest,omitempty"`
	ReviewCommentURL string `json:"review_comment_url,omitempty"`
	SelfReview       bool   `json:"self_review,omitempty"`
	// ReviewBound is nil (omitted from JSON) when no review applied at all
	// (a mechanical landing, round 3 minor 7). Once a review did apply, it
	// is a pointer to false when the recorded review named no commit to
	// bind to, or when the binding could not be verified (#586's
	// warn-still-land design, founder-decided 2026-09-18) — the landing
	// proceeds either way, this is never a refusal — but Evidence["review"]
	// carries the informational finding so the gap is visible rather than
	// silent. It is a pointer to true only once the binding is positively
	// confirmed.
	ReviewBound *bool `json:"review_bound,omitempty"`
	// Closes lists the issues GitHub's own closingIssuesReferences reports
	// this landing closes (#615). Empty is reported as the informational
	// "no linked issue" finding in Evidence["closes"], never a refusal.
	Closes []int `json:"closes,omitempty"`

	Checks *PullRequestWaitResult `json:"checks,omitempty"`

	BranchDeleted bool   `json:"branch_deleted"`
	LandingOnBase bool   `json:"landing_on_base"`
	CanonicalSync string `json:"canonical_sync,omitempty"`
	// LocalSync records the outcome of fast-forwarding the local WB worktree
	// (if any) after a server-side update-branch, or the reason it was left
	// alone (#611). Empty when no worktree holds the branch.
	LocalSync string `json:"local_sync,omitempty"`
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

	// LaneOwner is the landing-lane record this landing acquired, when the
	// caller populated Lane. It is nil when no guard ran.
	LaneOwner *landinglane.Record `json:"lane_owner,omitempty"`
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
	telemetry := &githubobserver.RetryTelemetry{}
	ctx = githubobserver.WithRetryTelemetry(ctx, telemetry)
	defer func() {
		if telemetry.Count > 0 {
			if result.Evidence == nil {
				result.Evidence = map[string]string{}
			}
			result.Evidence["github_read_retries"] = fmt.Sprintf("%d (last: %s)", telemetry.Count, telemetry.LastReason)
		}
		// A single transient GitHub read failure recovers in-process (see
		// githubobserver); only exhausting every in-process retry reaches
		// here, and the exact resume command replaces the raw "start over"
		// an agent would otherwise have to guess at.
		err = withPullRequestLandResumeGuidance(err, options, result)
		// Every outcome leaves exactly one event, including the error paths:
		// a verb that only records its successes produces a log in which
		// nothing ever goes wrong.
		appendLandEvent(options, result, started, err)
	}()
	return landPullRequest(ctx, options)
}

// withPullRequestLandResumeGuidance appends the exact resumable `wb pr land`
// invocation to an error that reached the caller only because every
// in-process retry for a transient GitHub read failure was exhausted. Any
// other error (an authoritative GitHub failure, a refusal, a validation
// error) is returned unchanged.
func withPullRequestLandResumeGuidance(err error, options PullRequestLandOptions, result PullRequestLandResult) error {
	if err == nil || !errors.Is(err, githubobserver.ErrTransientRetriesExhausted) {
		return err
	}
	number, numberErr := PullRequestNumber(options.PullRequest)
	if numberErr != nil {
		return err
	}
	// Round 3, B4: if this attempt already posted the identity form's
	// review comment before hitting the exhausted-retries error, the
	// resume command must carry the posted comment's URL, never the
	// identity and review text again — copy-running it must not post a
	// second comment re-approving whatever head is current when it runs.
	if strings.TrimSpace(result.ReviewCommentURL) != "" {
		options.ApprovedBy = result.ReviewCommentURL
		options.ReviewComment = ""
		options.ReviewCommentFile = ""
	}
	return fmt.Errorf("%w; resumable: %s", err, pullRequestLandResumeCommand(options, number, ""))
}

// pullRequestLandResumeCommand rebuilds the exact `wb pr land` invocation
// that recovers a landing left incomplete - by exhausted transient GitHub
// read retries, or by a checks-pending timeout (#584) - carrying forward
// every option that changes what the command does. timeoutFlag is the
// --timeout value to print; a caller with no opinion on it (the transient-
// retry resume, which is not a budget problem) passes "".
func pullRequestLandResumeCommand(options PullRequestLandOptions, number, timeoutFlag string) string {
	parts := []string{"wb", "pr", "land", options.Repository + "#" + number}
	if timeoutFlag != "" {
		parts = append(parts, "--timeout", timeoutFlag)
	}
	if options.MergeMethodExplicit && strings.TrimSpace(options.MergeMethod) != "" {
		parts = append(parts, "--merge-method", options.MergeMethod)
	}
	if options.Keep {
		parts = append(parts, "--keep")
	}
	if options.NoAutoMerge {
		parts = append(parts, "--no-auto-merge")
	}
	if options.AllowUnfenced {
		parts = append(parts, "--allow-unfenced")
	}
	if len(options.KeepCommits) > 0 {
		parts = append(parts, "--keep-commits", strings.Join(options.KeepCommits, ","))
	}
	if strings.TrimSpace(options.Reason) != "" {
		parts = append(parts, "--reason", strconv.Quote(options.Reason))
	}
	if strings.TrimSpace(options.Subject) != "" {
		parts = append(parts, "--subject", strconv.Quote(options.Subject))
	}
	if strings.TrimSpace(options.ApprovedBy) != "" {
		parts = append(parts, "--approved-by", strconv.Quote(options.ApprovedBy))
	}
	if strings.TrimSpace(options.ReviewComment) != "" {
		parts = append(parts, "--review-comment", strconv.Quote(options.ReviewComment))
	}
	if strings.TrimSpace(options.ReviewCommentFile) != "" {
		parts = append(parts, "--review-comment-file", strconv.Quote(options.ReviewCommentFile))
	}
	return strings.Join(parts, " ")
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
		options.MergeMethod = "merge"
	}
	switch options.MergeMethod {
	case "squash", "merge", "rebase":
	default:
		return PullRequestLandResult{}, fmt.Errorf("unsupported merge method %q; use squash, merge, or rebase", options.MergeMethod)
	}
	if strings.TrimSpace(options.Subject) != "" && options.MergeMethod != "squash" {
		return PullRequestLandResult{}, fmt.Errorf("--subject requires --merge-method squash")
	}
	if len(options.KeepCommits) > 0 && (!options.MergeMethodExplicit || options.MergeMethod != "squash") {
		return PullRequestLandResult{}, fmt.Errorf("--keep-commits requires explicit --merge-method squash")
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
			manualPullRequestMergeCommand(options.Repository, number, options.MergeMethod),
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

	// The landing-lane guard runs before any check wait or merge attempt: a
	// different live session already driving this (repository, target) lane
	// must be refused before this call spends its CI-wait budget on a target
	// it does not own landing onto. See LaneGuardRequest. `wb pr land`
	// carries no resumable receipt across invocations, so the lane this
	// acquires is released unconditionally once this call returns.
	laneRecord, laneErr := acquireLandingLane(options.ProjectsRoot, options.Repository, view.Base.Ref, options.Lane)
	if laneErr != nil {
		var conflict *landinglane.ConflictError
		if errors.As(laneErr, &conflict) {
			return mergeRefusal(result, landRefusal{
				code:    LandRefusalLandingLaneHeld,
				reason:  laneErr.Error(),
				command: "wb session recall " + conflict.Record.Owner.WBSessionID,
			}), nil
		}
		return result, laneErr
	}
	if laneRecord.Owner.WBSessionID != "" {
		result.LaneOwner = &laneRecord
		defer func() {
			_ = releaseLandingLane(options.ProjectsRoot, options.Repository, view.Base.Ref, laneRecord.Owner.WBSessionID)
		}()
	}

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
	// reviewedHead is the head this landing binds any review to (#586): the
	// exact head observed before any update-branch cycle runs. A review
	// supplied for a different head — whether the identity form (posted
	// against this head) or a back-compat file/URL (assumed to describe
	// this head, since nothing else names one) — is stale the moment the
	// head advances by anything other than WB's own update-branch merges.
	reviewedHead := result.HeadSHA
	hasReviewComment := strings.TrimSpace(options.ReviewComment) != "" || strings.TrimSpace(options.ReviewCommentFile) != ""
	// pendingIdentity/pendingComment (round 3, B4) defer actually POSTING the
	// identity form's review comment until after the preflight cleanup check
	// passes, and it happens exactly once per invocation: classification
	// only validates and resolves what it can without side effects, so a
	// preflight refusal right after never leaves a comment posted for a
	// landing that did not happen, and nothing downstream can re-run this
	// switch and post a second one.
	var pendingIdentity *ReviewerIdentity
	var pendingComment string
	if !result.Mechanical {
		kind := classifyApprovedBy(result.ApprovedBy, hasReviewComment)
		switch kind {
		case approvalKindEmpty:
			return mergeRefusal(result, landRefusal{
				code: LandRefusalUnapprovedPatch,
				reason: "this change is not a mechanical dependency bump (" + verdict.Summary() +
					"), so it needs a recorded review approval before it can land",
				command: "wb pr land " + options.Repository + "#" + number + " --approved-by <review-file-or-comment-url>",
			}), nil
		case approvalKindCI:
			return mergeRefusal(result, landRefusal{
				code:    LandRefusalUnapprovedPatch,
				reason:  "--approved-by ci is not implemented yet; follow-up: https://github.com/sneat-dev/wb/issues/619",
				command: "wb pr land " + options.Repository + "#" + number + " --approved-by <review-file-or-comment-url-or-reviewer-identity>",
			}), nil
		case approvalKindIdentity:
			identity := FinalizeReviewerIdentity(FillReviewerIdentityFromEnvironment(ParseReviewerIdentity(result.ApprovedBy)))
			comment, commentErr := readReviewCommentText(options.ReviewComment, options.ReviewCommentFile)
			if commentErr != nil {
				return result, commentErr
			}
			if comment == "" {
				return mergeRefusal(result, landRefusal{
					code:   LandRefusalReviewCommentEmpty,
					reason: "--approved-by " + result.ApprovedBy + " is a reviewer identity and needs a non-empty --review-comment or --review-comment-file",
					command: "wb pr land " + options.Repository + "#" + number + " --approved-by " + result.ApprovedBy +
						" --review-comment \"<the review>\"",
				}), nil
			}
			pendingIdentity = &identity
			pendingComment = comment
		case approvalKindFile, approvalKindURL:
			// #586 (founder-decided 2026-09-18: warn, still land): a file or
			// comment review binds to whatever commit its own
			// "Reviewed-Head: <sha>" line names — read now, from the review
			// artifact itself, so a foreign push that happened between when
			// the review was written and this invocation is still caught.
			// A review that names no head is never refused for it; it lands,
			// with the gap surfaced as the "review-unbound" finding. A
			// malformed line, or (URL kind) a comment naming a different
			// repository or pull request, IS refused (round 3, minors 1/2):
			// those are cases where the review artifact plainly asserts
			// something that does not check out, never silently downgraded
			// to "unbound".
			named, namedErr := namedReviewedHead(ctx, result.ApprovedBy, kind, options.Repository, number)
			if namedErr != nil {
				if errors.Is(namedErr, errReviewedHeadCrossRepository) {
					return mergeRefusal(result, landRefusal{
						code: LandRefusalReviewCommentCrossRepo,
						reason: "the review comment URL " + result.ApprovedBy +
							" names a different repository or pull/issue than " + options.Repository + "#" + number,
						command: "wb pr land " + options.Repository + "#" + number + " --approved-by <a comment URL on this pull request>",
					}), nil
				}
				return mergeRefusal(result, landRefusal{
					code: LandRefusalReviewHeadMalformed,
					reason: "the review named by --approved-by " + result.ApprovedBy +
						" has a \"Reviewed-Head:\" line that is not a full 40-character SHA",
					command: "wb pr land " + options.Repository + "#" + number + " --approved-by " + result.ApprovedBy,
				}), nil
			}
			if named != "" {
				reviewedHead = named
				result.ReviewedHeadSHA = named
				result.ReviewBound = boolPtr(true)
			} else {
				result.ReviewBound = boolPtr(false)
				result.Evidence["review"] = "review-unbound: the review does not name the commit it reviewed; add \"Reviewed-Head: <sha>\""
			}
		}
	}

	// Pre-flight the cleanup now, while refusing is still free, and BEFORE
	// arming auto-merge: once armed, GitHub merges on green whatever this
	// process later decides, so a guard that runs after arming guards nothing.
	// Discovering after the merge that the worktree cannot be retired leaves the
	// landing done and the tidy-up impossible, which is the shape that produced
	// sixty abandoned checkouts in the first place.
	// The live-link half of this runs whatever --keep says. --keep opts out of
	// retiring the worktree; it does not opt out of the rule that a worktree
	// building against an unpublished tree must not be landed, and reading it
	// as a bypass would make the guard optional by accident.
	reportPullRequestLandProgress(options.OperationProgress, "preflight_cleanup", progress.Started, view.Head.Ref, 0, 0)
	if refusal := preflightLandingCleanup(ctx, options, view, number, options.Keep); refusal != nil {
		return mergeRefusal(result, *refusal), nil
	}
	reportPullRequestLandProgress(options.OperationProgress, "preflight_cleanup", progress.Completed, view.Head.Ref, 0, 0)

	// The identity form's review comment is posted here — after the
	// preflight passes, before anything is armed — and exactly once (round
	// 3, B4). Posting any earlier risked a comment for a landing that then
	// refused on the preflight; posting is also the point at which
	// result.ApprovedBy (and options.ApprovedBy, mutated below so every
	// resume command built from here on reflects it) switches from the
	// identity+comment form to the posted comment's URL. WB's own
	// resume/sanctioned commands from here on use only that URL and never
	// again echo the review text — both because a second run must not post
	// a second comment re-approving whatever head is current by then, and
	// because strconv.Quote does not escape "$" or a backtick, so a review
	// comment containing either would shell-substitute if a sanctioned
	// command carrying it were copy-run.
	if pendingIdentity != nil {
		commentURL, postErr := postReviewComment(ctx, options.Repository, number, *pendingIdentity, reviewedHead, pendingComment)
		if postErr != nil {
			return result, postErr
		}
		result.Reviewer = pendingIdentity.String()
		result.ReviewedHeadSHA = reviewedHead
		result.ReviewBound = boolPtr(true)
		result.ReviewDigest = ReviewDigest(pendingComment)
		result.ReviewCommentURL = commentURL
		result.SelfReview = pendingIdentity.SelfReview(currentSessionIdentity())
		result.ApprovedBy = commentURL
		result.Evidence["reviewer"] = pendingIdentity.String()
		result.Evidence["review_comment_url"] = commentURL
		if result.SelfReview {
			result.Evidence["self_review"] = "true"
		}
		options.ApprovedBy = commentURL
		options.ReviewComment = ""
		options.ReviewCommentFile = ""
	}

	// #586/round 3 (B3): a named review must be checked BEFORE auto-merge is
	// armed, not after — once armed, GitHub can merge on green at any time
	// this process does not control, so a staleness check that runs only
	// after arming can lose the race to GitHub's own merge. reviewedHead
	// still equals view.Head.SHA here for the identity form (it was just
	// posted against this exact head) and for a mechanical/no-review
	// landing (reviewedHead == "" or unset); it can differ for a back-compat
	// file/URL review naming an older head, which is exactly the case this
	// guards. autoMergeArmed is always false here — arming has not happened
	// yet — so the refusal never claims an armed state it has not reached.
	if !result.Mechanical && reviewedHead != "" && reviewedHead != view.Head.SHA {
		if refusal, note := reviewStaleRefusal(ctx, options, view, reviewedHead, view.Head.SHA, false, number); refusal != nil {
			return mergeRefusal(result, *refusal), nil
		} else if note != "" {
			result.ReviewBound = boolPtr(false)
			result.Evidence["review"] = "review-unverified: " + note
		}
	}

	// The commit message is settled before arming too, because when GitHub
	// performs the merge it uses the message it was armed with. It is also read
	// before any update-branch, so GitHub's own "Merge main into …" commit never
	// becomes part of the aggregated body.
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

	// Arm auto-merge before waiting and then wait, bringing the candidate up
	// to date whenever the target moves under it — the shared engine both
	// this verb and the worktree-merge PR route drive. See
	// awaitLandablePullRequest for why arming happens first and why this is
	// a loop rather than one check before one wait.
	var (
		waited         PullRequestWaitResult
		mergedByGitHub bool
		updateRefusal  *landRefusal
	)
	view, waited, result.AutoMergeArmed, mergedByGitHub, updateRefusal, err = awaitLandablePullRequest(ctx, options, view, number, subject, body, result.Evidence)
	// Record the exact head this attempt last observed on every path - the
	// refusal and error returns below included - so a caller reading the
	// result can see what it landed against even when the attempt did not
	// finish landing.
	result.HeadSHA = view.Head.SHA
	// local_sync is copied to the typed LocalSync field and removed from the
	// evidence map so it does not also leak as a stray evidence.local_sync
	// key into `wb pr land --json`.
	result.LocalSync = result.Evidence["local_sync"]
	delete(result.Evidence, "local_sync")
	if err != nil {
		return result, err
	}
	if updateRefusal != nil {
		return mergeRefusal(result, *updateRefusal), nil
	}
	result.Checks = &waited
	result.AbsorbedPolls = waited.StableObservations
	if mergedByGitHub {
		waited.Status = PullRequestWaitPassed
		result.Evidence["merged_by"] = "github auto-merge"
	}
	switch waited.Status {
	case PullRequestWaitPassed:
	case PullRequestWaitPending:
		result.Outcome = LandFindings
		result.RefusalCode = LandRefusalChecksPending
		result.Reason = waited.Reason + "; run the resume command in the background - it carries a budget above the foreground harness ceiling"
		result.SanctionedCommand = pullRequestLandResumeCommand(options, number, prLandResumeTimeoutFlag(options.Slice))
		// Auto-merge was armed before the wait, so a pending result is not a
		// stranded change: GitHub lands it when the checks pass. Say so, and
		// name the one part GitHub cannot do.
		if result.AutoMergeArmed {
			result.Reason = waited.Reason + "; auto-merge is armed, so this lands without WB once checks pass"
			result.SanctionedCommand = "wb worktree gc --apply  (after GitHub merges it)"
			if options.Keep {
				result.SanctionedCommand = "none: GitHub merges it when the checks pass"
			}
		}
		return withSavings(result), nil
	default:
		result.Outcome = LandFindings
		result.RefusalCode = LandRefusalChecksFailed
		result.Reason = waited.Reason
		// #600: point at the failing job directly rather than the PR page,
		// which names nothing and makes the caller re-derive which check and
		// which job actually failed.
		result.SanctionedCommand = checksFailedSanctionedCommand(waited.FailureDetails, options.Repository, number)
		// Auto-merge stays armed through a red result. CI is the gate: whoever
		// pushes a fix is responsible for it, the required checks re-run
		// against what they pushed, and the merge happens only if they pass.
		// A review that must not be skippable belongs in the workflow, where
		// it runs on every push, rather than in an approval recorded once
		// against a head that no longer exists.
		if result.AutoMergeArmed {
			result.Reason = waited.Reason + "; auto-merge stays armed, so a pushed fix lands once the required checks pass"
		}
		if strings.Contains(waited.Reason, "strict up-to-date fence") {
			// This is a policy gap, not a red check: say which, and name the
			// widening rather than leaving the operator to guess that green
			// checks were not the problem.
			result.RefusalCode = LandRefusalUnfencedTarget
			result.SanctionedCommand = "wb pr land " + options.Repository + "#" + number + " --allow-unfenced"
		} else if summary := summarizeCheckFailures(waited.FailureDetails); summary != "" {
			// #600: name each failing check and its first error line rather
			// than leaving the caller to hand-roll the same log scraping WB
			// already did while observing the checks.
			result.Reason += "; " + summary
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

	// #586: a review — identity, file, or URL — authorizes landing exactly
	// the head it was recorded against. WB's own update-branch merges are
	// the one exception (proved by reviewedHeadStillCurrent); a foreign
	// push, a fix commit, or a force-push in between is not. This is the
	// post-wait half of the check (round 3, B3): the pre-arm check above
	// already covers everything up to the moment auto-merge was armed; this
	// one catches a foreign push that landed DURING the wait, while
	// !mergedByGitHub still means this process, not GitHub's armed
	// auto-merge, performs the merge write below — so refusing here still
	// prevents it.
	if !result.Mechanical && !mergedByGitHub && reviewedHead != "" {
		if refusal, note := reviewStaleRefusal(ctx, options, view, reviewedHead, view.Head.SHA, result.AutoMergeArmed, number); refusal != nil {
			return mergeRefusal(result, *refusal), nil
		} else if note != "" {
			result.ReviewBound = boolPtr(false)
			result.Evidence["review"] = "review-unverified: " + note
		}
	}
	// mergedByGitHub means GitHub's armed auto-merge already landed the
	// current head before this process's own check could run — the merge
	// cannot be undone, so this is never a refusal (round 3, B3's third
	// bullet). recordMergedByGitHubReviewBinding verifies the merge is
	// provably covered before letting the receipt claim it is.
	if !result.Mechanical && mergedByGitHub && reviewedHead != "" {
		recordMergedByGitHubReviewBinding(ctx, options, view, reviewedHead, &result)
	}

	head := view.Head.SHA
	mergeMethod := options.MergeMethod

	if len(options.KeepCommits) > 0 && !mergedByGitHub {
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
		rewritten := PullRequestWaitOptions{
			Repository:        options.Repository,
			PullRequest:       number,
			Target:            view.Base.Ref,
			AllowUnfenced:     options.AllowUnfenced,
			Slice:             remainingWaitBudget(options, waitDeadline(options)),
			CheckPollInterval: options.CheckPollInterval,
			Progress:          options.Progress,
			OperationProgress: options.OperationProgress,
			Head:              keptHead,
		}
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

	result.ManualEquivalent[3] = manualPullRequestMergeCommand(options.Repository, number, mergeMethod)
	mergeSHA, mergeCallRefusal, mergeErr := mergeOrAdoptAutoMerge(ctx, options, number, head, mergeMethod, subject, body, result.AutoMergeArmed, mergedByGitHub, result.Evidence)
	if mergeErr != nil {
		return result, mergeErr
	}
	if mergeCallRefusal != nil {
		return mergeRefusal(result, *mergeCallRefusal), nil
	}
	if mergeSHA != "" {
		result.MergeSHA = mergeSHA
	}

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

	reportPullRequestLandProgress(options.OperationProgress, "sync_canonical", progress.Started, view.Base.Ref+"@"+shortMergeRevision(landed.MergeCommitSHA), 0, 0)
	canonical, canonicalErr := worktrees.CanonicalRepositoryPath(options.ProjectsRoot, options.Repository)
	if canonicalErr != nil {
		result.Outcome = LandFindings
		result.RefusalCode = LandRefusalCanonicalSync
		result.Reason = canonicalErr.Error()
		result.SanctionedCommand = "wb sync --filter " + options.Repository
		return withSavings(result), nil
	}
	result.CanonicalSync, err = syncCanonicalMergeTarget(ctx, canonical, view.Base.Ref, landed.MergeCommitSHA, options.Slice, 0, options.CheckoutUpdated)
	if err != nil {
		result.Outcome = LandFindings
		result.RefusalCode = LandRefusalCanonicalSync
		result.Reason = err.Error()
		result.SanctionedCommand = "wb sync --filter " + options.Repository
		return withSavings(result), nil
	}
	reportPullRequestLandProgress(options.OperationProgress, "sync_canonical", progress.Completed, result.CanonicalSync, 0, 0)

	reportPullRequestLandProgress(options.OperationProgress, "delete_remote_branch", progress.Started, view.Head.Ref, 0, 0)
	if deleted, deleteErr := deleteRemoteBranch(ctx, options.Repository, view, landed); deleteErr != nil {
		return result, deleteErr
	} else {
		result.BranchDeleted = deleted
	}
	reportPullRequestLandProgress(options.OperationProgress, "delete_remote_branch", progress.Completed, view.Head.Ref, 0, 0)

	if !options.Keep {
		reportPullRequestLandProgress(options.OperationProgress, "cleanup", progress.Started, view.Head.Ref, 0, 0)
		tasks, reports, cleanupErr := cleanupLandedWorktrees(ctx, options.ProjectsRoot, options.Repository, view.Head.Ref, view.Head.SHA, view.Base.Ref, landed.MergeCommitSHA)
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

	// #615: report the issues this landing closes, from GitHub's own
	// closingIssuesReferences — an informational finding, never a refusal,
	// including when there are none.
	if issues, closesErr := closingIssuesReferences(ctx, options.Repository, number); closesErr == nil {
		result.Closes = issues
		result.Evidence["closes"] = formatClosesFinding(issues)
	} else {
		result.Evidence["closes_error"] = closesErr.Error()
	}

	result.Outcome = LandSuccess
	return withSavings(result), nil
}

func manualPullRequestMergeCommand(repository, number, method string) string {
	return "gh pr merge " + number + " --repo " + repository + " --" + method
}

// checksFailedSanctionedCommand names the command that shows the actual
// failure (#600), rather than the PR page, which shows nothing about which
// check or job failed. SanctionedCommand must be a runnable command, never a
// bare URL: a check run's Link is a GitHub Actions job URL, but a commit
// status's Link is the provider-controlled TargetURL, which is not
// necessarily one, and is never safe to print as "the command" (#584 round
// 3, minor 8). So this only ever returns a gh invocation, built from a
// GitHub Actions run/job URL when the first failing check's Link parses as
// one, else the previous PR-page fallback.
func checksFailedSanctionedCommand(details []CIFailureDetail, repository, number string) string {
	if len(details) > 0 {
		if runID, jobID, ok := githubActionsRunAndJob(details[0].JobURL); ok {
			return "gh run view " + runID + " --job " + jobID + " --repo " + repository + " --log-failed"
		}
	}
	return "gh pr view " + number + " --repo " + repository + " --web"
}

// recommendedPRLandResumeTimeout is the budget named on a checks-pending
// resume (#584). Measured CI wall-clock on this fleet ranges 3-11 minutes for
// sneat-co/sneat-go and around 8 minutes for sneat-dev/wb, so the identical
// invocation this refusal used to print - no --timeout, which defaults to
// defaultCIWaitSlice (8 minutes) - could time out again about as often as it
// succeeds. 45 minutes clears the measured range with headroom. Foreground
// calls must still respect the harness's own ~10 minute ceiling; a caller
// that needs 45 minutes backgrounds the resume rather than waiting on it.
const recommendedPRLandResumeTimeout = 45 * time.Minute

// prLandResumeTimeoutFlag names the --timeout value a checks-pending resume
// should carry: the budget already in effect when it is at least the
// recommended floor, or the recommended floor itself when the effective
// budget was smaller (the common case, since defaultCIWaitSlice undercuts
// it). Re-running the identical invocation that just timed out, with no
// --timeout at all, cannot converge.
func prLandResumeTimeoutFlag(inEffect time.Duration) string {
	timeout := inEffect
	if timeout < recommendedPRLandResumeTimeout {
		timeout = recommendedPRLandResumeTimeout
	}
	return formatPRLandTimeoutFlag(timeout)
}

// formatPRLandTimeoutFlag renders a duration as a --timeout value a caller
// would actually type. time.Duration.String() renders 45*time.Minute as
// "45m0s"; whole minutes render as "<N>m" instead, and anything else falls
// back to the standard rendering.
func formatPRLandTimeoutFlag(d time.Duration) string {
	if d > 0 && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int64(d/time.Minute))
	}
	return d.String()
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

// boolPtr is PullRequestLandResult.ReviewBound's constructor: a pointer so
// the receipt can distinguish "no review applied" (nil, omitted from JSON)
// from a review that applied but is not (yet, or provably) bound (false).
func boolPtr(value bool) *bool {
	return &value
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
// Work Log seal — and passes the exact PR head, base, and landing commit as a
// receipt. That proof may supersede an earlier manifest base in memory without
// rewriting the append-only creation record.
func cleanupLandedWorktrees(ctx context.Context, projectsRoot, repository, headRef, headSHA, base, landingSHA string) ([]string, []string, error) {
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
	proofsByTask := map[string][]worktrees.MergeReceiptCleanupProof{}
	for _, entry := range listed.Results {
		if entry.Repository != repository || entry.Branch != headRef {
			continue
		}
		proofsByTask[entry.Task] = append(proofsByTask[entry.Task], worktrees.MergeReceiptCleanupProof{
			Repository: repository, Target: base, SourceTask: entry.Task,
			SourceWorktree: entry.WorktreeDir, SourceBranch: headRef, SourceSHA: headSHA,
			CandidateSHA: headSHA, LandingSHA: landingSHA,
		})
		if !seen[entry.Task] {
			seen[entry.Task] = true
			tasks = append(tasks, entry.Task)
		}
	}
	cleaned := make([]string, 0, len(tasks))
	reports := make([]string, 0, len(tasks))
	for _, task := range tasks {
		outcome, cleanupErr := worktrees.Cleanup(ctx, worktrees.CleanupOptions{
			ProjectsRoot:       projectsRoot,
			Tasks:              []string{task},
			ExactRepository:    repository,
			Base:               base,
			AbsorbedBy:         landingSHA,
			MergeReceiptProofs: proofsByTask[task],
			Apply:              true,
			OlderThan:          0,
			Workers:            1,
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
