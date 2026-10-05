package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/mechanicalchange"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/prmeta"
	"github.com/sneat-dev/wb/internal/repopath"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// wb pr create opens a pull request for one worktree without running the
// local suite `wb worktree merge`/`wb worktree land` pays for first (#591).
// It exists because that cost — 26+ minutes on a shared 4-core VM — pushed
// agents onto raw `gh pr create`, which loses worktree-pinned pushes (a wrong
// repository has been pushed to by hand before), WB-authored PR text,
// duplicate-PR adoption, and the task -> pull-request binding the daemon needs
// to resolve a pull request back to its owning session. CI is the gate here;
// nothing in this file runs a build, a test, or a lint pass.

// CreateRefusal codes are the machine-readable half of a refusal.
const (
	CreateRefusalCanonicalClone        = "canonical-clone"
	CreateRefusalDirtyWorktree         = "dirty-worktree"
	CreateRefusalNothingToPush         = "nothing-to-push"
	CreateRefusalUnapprovedPatch       = "unapproved-patch-set"
	CreateRefusalNothingStaged         = "nothing-staged"
	CreateRefusalSecretPath            = "secret-like-path"
	CreateRefusalLeftoverBeforeLanding = "leftover-before-landing"
	CreateRefusalInvalidPath           = "invalid-add-path"
	CreateRefusalBaseMismatch          = "base-mismatch"
	// CreateRefusalIdentityNeedsLand reports the reviewer-identity form of
	// --approved-by given to --auto-merge without --land (round 3, MAJOR
	// fix for #604's `pr create` gap): --auto-merge alone never posts the
	// review comment the identity form requires, so accepting it silently
	// would arm auto-merge on an unrecorded "review".
	CreateRefusalIdentityNeedsLand = "identity-review-needs-land"
)

// CreateOutcome is the envelope outcome, mapped onto WB's exit-code contract
// by ExitCode: success is 0, findings is 1, refused is 2, landed-incomplete is
// 3 (--land merged, but its follow-up did not finish).
type CreateOutcome string

const (
	CreateSuccess  CreateOutcome = "success"
	CreateFindings CreateOutcome = "findings"
	CreateRefused  CreateOutcome = "refused"
	// CreateLandedIncomplete mirrors LandLandedIncomplete for --land.
	CreateLandedIncomplete CreateOutcome = "landed-incomplete"
)

// PullRequestCreateOptions identifies the worktree to open a pull request for.
type PullRequestCreateOptions struct {
	// Worktree is a linked worktree path or a task name. Empty resolves to
	// the current directory.
	Worktree     string
	ProjectsRoot string
	Title        string
	// Body is literal pull-request body text, as `gh pr create --body` takes
	// it. Mutually exclusive with BodyFile; the caller validates that.
	Body     string
	BodyFile string
	Draft    bool
	// Base overrides the pull request's target branch. Empty uses the
	// worktree's own recorded base.
	Base string
	// Closes lists issue numbers this pull request closes; one "Closes #N"
	// line per issue is written at the top of the body (#615). Never
	// populated from the task's prompt automatically — see
	// SuggestClosesFromPrompt, which the caller decides whether to act on.
	Closes []int

	// AutoMerge arms GitHub auto-merge immediately after the pull request is
	// created or adopted, under the same authority `wb pr land` requires to
	// arm it: a mechanical diff, or a non-mechanical one with ApprovedBy, and
	// a target whose policy AllowUnfenced does not have to widen.
	AutoMerge     bool
	ApprovedBy    string
	AllowUnfenced bool
	MergeMethod   string
	// Lane optionally names the acquiring WB session for the same
	// landing-lane guard `wb pr land` acquires around arming; see
	// LaneGuardRequest. A zero value skips the guard, exactly as it does for
	// LandPullRequest. Only meaningful with AutoMerge; --land threads its own
	// Lane through LandOptions instead.
	Lane LaneGuardRequest

	// CommitStaged commits exactly the worktree's index. It refuses when
	// nothing is staged. CommitAll runs `git add -A` first (respecting
	// .gitignore, and refusing any path that looks like a secret), then
	// commits everything. They are mutually exclusive; the caller validates
	// that. Message is required with either one and is used verbatim.
	// Add commits exactly the named paths: `git add -- <paths>`, then
	// `git commit -m Message -- <paths>`, so anything else already staged is
	// not included. It is a third commit mode, mutually exclusive with
	// CommitStaged and CommitAll; the caller validates that.
	Add          []string
	CommitStaged bool
	CommitAll    bool
	Message      string

	// Land lands the pull request in-process through the existing
	// `LandPullRequest`, using LandOptions as a template: Repository and
	// PullRequest are filled in here once both are known. `--land` implies
	// arming, the way `wb pr land` itself does, so AutoMerge is not armed
	// separately when Land is set.
	Land        bool
	LandOptions *PullRequestLandOptions

	// LinkPreflight runs immediately before arming auto-merge or landing,
	// exactly as `wb pr land` runs `refuseLinkedRepositoryWorktrees` before
	// its own GitHub calls. It lives in cmd/wb, not here, so it is injected
	// rather than imported: internal/orchestrate must not depend on cmd/wb.
	LinkPreflight func(repository string) error

	Timeout time.Duration
	Retry   int

	// Events receives one structured record per invocation, whatever the
	// outcome. A nil appender discards. EventsForRepository, when set,
	// overrides it once the repository is resolved: this verb's own
	// repository is not known until the worktree's manifest is read, unlike
	// `wb pr land`, which receives it as its own CLI argument up front, so
	// cmd/wb cannot resolve the repository-scoped stream ahead of the call
	// the way it does for `wb pr land`. --land also uses it to give the
	// nested `LandPullRequest` call the same repository-scoped stream
	// `wb pr land` itself would have used.
	Events              streams.EventAppender
	Stream              string
	EventsForRepository func(repository string) (streams.EventAppender, string)

	// run overrides this package's generic command runner (ports.go); nil
	// uses defaultRunner. A unit test sets this to a runnertest.Fake so a
	// create call that reaches the migrated call sites
	// (spec/plans/coverage-to-100 task-17) never starts a real process.
	run runner.Runner
}

// resolveRunner returns options.run, falling back to defaultRunner
// (ports.go) when the caller left it nil.
func (options PullRequestCreateOptions) resolveRunner() runner.Runner {
	if options.run != nil {
		return options.run
	}
	return defaultRunner
}

// PullRequestCreateResult is the receipt, and the JSON envelope.
type PullRequestCreateResult struct {
	SchemaVersion     int           `json:"v"`
	Verb              string        `json:"verb"`
	Outcome           CreateOutcome `json:"outcome"`
	RefusalCode       string        `json:"refusal_code,omitempty"`
	SanctionedCommand string        `json:"sanctioned_command,omitempty"`
	Reason            string        `json:"reason,omitempty"`

	Repository  string `json:"repository,omitempty"`
	PullRequest int    `json:"pull_request,omitempty"`
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	HeadRef     string `json:"head_ref,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
	BaseRef     string `json:"base_ref,omitempty"`
	// Adopted records that an already-open pull request for this branch was
	// reused rather than a new one created.
	Adopted bool `json:"adopted,omitempty"`

	Mechanical bool   `json:"mechanical"`
	ApprovedBy string `json:"approved_by,omitempty"`

	AutoMergeArmed  bool   `json:"auto_merge_armed,omitempty"`
	AutoMergeReason string `json:"auto_merge_not_armed_reason,omitempty"`

	// NextCommand is the exact follow-up invocation: `wb pr land` unless
	// AutoMerge was requested, in which case it is `wb wait pr`.
	NextCommand string `json:"next_command,omitempty"`

	// Task and ClaimID identify the durable Work Log claim the pull request
	// was bound to, when binding succeeded.
	Task    string `json:"task,omitempty"`
	ClaimID string `json:"claim_id,omitempty"`

	// CommittedPaths lists every path a requested --commit-staged/--commit-all
	// committed, in the order `git diff-tree` reports them.
	CommittedPaths []string `json:"committed_paths,omitempty"`

	// LandResult is populated when Land was requested: the full receipt of the
	// in-process `LandPullRequest` call this invocation's own Outcome mirrors.
	LandResult *PullRequestLandResult `json:"land_result,omitempty"`

	Evidence map[string]string `json:"evidence,omitempty"`
}

// ExitCode maps the outcome onto WB's exit contract.
func (result PullRequestCreateResult) ExitCode() int {
	switch result.Outcome {
	case CreateSuccess:
		return 0
	case CreateRefused:
		return 2
	case CreateLandedIncomplete:
		return ExitLandedIncomplete
	default:
		return 1
	}
}

// createRefusal is the internal shape a guard fills in before it is merged
// into the result.
type createRefusal struct {
	code    string
	reason  string
	command string
}

func mergeCreateRefusal(result PullRequestCreateResult, refusal createRefusal) PullRequestCreateResult {
	result.Outcome = CreateRefused
	result.RefusalCode = refusal.code
	result.Reason = refusal.reason
	result.SanctionedCommand = refusal.command
	return result
}

// CreatePullRequest resolves one worktree, pushes its branch through the
// normal push path, and opens or adopts its pull request. It performs no
// local build, test, or lint: CI is the gate, and the caller that wants a
// verified merge runs `wb pr land` on the number this prints.
func CreatePullRequest(ctx context.Context, options PullRequestCreateOptions) (result PullRequestCreateResult, err error) {
	started := time.Now()
	defer func() {
		// An error returned after the commit step (a push failure, a failed
		// adopt/create, an arming error, ...) must still carry whatever the
		// invocation already did — CommittedPaths, and LandResult when
		// --land got far enough to have one — so a caller that always prints
		// the envelope (as `wb pr create --format json` does) reports a
		// finding instead of losing that state to a bare error.
		if err != nil && result.Outcome == "" {
			result.Outcome = CreateFindings
			result.Reason = err.Error()
		}
		events, streamName := options.Events, options.Stream
		if options.EventsForRepository != nil {
			events, streamName = options.EventsForRepository(result.Repository)
		}
		appendCreateEvent(events, streamName, result, started, err)
	}()
	result, err = createPullRequest(ctx, options)
	return result, err
}

func createPullRequest(ctx context.Context, options PullRequestCreateOptions) (PullRequestCreateResult, error) {
	result := PullRequestCreateResult{SchemaVersion: 1, Verb: "pr create", Evidence: map[string]string{}}

	worktreeArg := strings.TrimSpace(options.Worktree)
	if worktreeArg == "" {
		worktreeArg = "."
	}
	worktree, err := resolvePullRequestCreateWorktree(ctx, options.ProjectsRoot, worktreeArg)
	if err != nil {
		return result, err
	}

	guard, err := worktrees.Guard(ctx, worktree, worktrees.GuardOptions{ProjectsRoot: options.ProjectsRoot})
	if err != nil {
		return result, err
	}
	if guard.Kind == "canonical" {
		return mergeCreateRefusal(result, createRefusal{
			code:    CreateRefusalCanonicalClone,
			reason:  fmt.Sprintf("%s is the canonical clone; a pull request is opened from a linked worktree, never from it", guard.CanonicalDir),
			command: "wb worktree create <task> <owner/repository>",
		}), nil
	}
	// The guard resolves to the worktree's own root, whatever subdirectory
	// worktree named: every later step (the base fetch, the manifest read,
	// the claim binding, and the leftover/dirty checks) must run pinned to
	// that root, not to wherever the caller happened to run the command from.
	if guard.Path != "" {
		worktree = guard.Path
	}

	if options.CommitStaged || options.CommitAll || len(options.Add) > 0 {
		refusal, committed, commitErr := performPullRequestCreateCommit(ctx, worktree, options)
		if refusal != nil {
			return mergeCreateRefusal(result, *refusal), nil
		}
		if commitErr != nil {
			return result, commitErr
		}
		result.CommittedPaths = committed
	}

	dirtyPaths, err := pullRequestCreateDirtyPaths(ctx, worktree)
	if err != nil {
		return result, err
	}
	if len(dirtyPaths) > 0 {
		return mergeCreateRefusal(result, createRefusal{
			code:    CreateRefusalDirtyWorktree,
			reason:  "worktree has uncommitted changes: " + strings.Join(describeStatusEntries(dirtyPaths), ", "),
			command: "commit or discard the listed paths, then retry",
		}), nil
	}

	manifest, manifestErr := worktrees.ReadManifest(worktree)
	repository := ""
	base := strings.TrimSpace(options.Base)
	if manifestErr == nil {
		repository = strings.TrimSpace(manifest.Repository)
		if base == "" {
			base = strings.TrimSpace(manifest.Base)
		}
	}
	if repository == "" {
		address, addressErr := repopath.FromLocalPath(options.ProjectsRoot, guard.CanonicalDir)
		if addressErr != nil {
			return result, fmt.Errorf("resolve repository for %s: %w", worktree, addressErr)
		}
		repository = address.Slug()
	}
	if base == "" {
		base = "main"
	}
	branch := guard.Branch
	result.Repository = repository
	result.BaseRef = base
	result.HeadRef = branch

	if _, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "fetch", "--no-tags", "origin", base); err != nil {
		return result, fmt.Errorf("fetch base branch %s: %w", base, err)
	}
	subjectsRaw, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "log", "--format=%s", "origin/"+base+"..HEAD")
	if err != nil {
		return result, fmt.Errorf("read commits ahead of %s: %w", base, err)
	}
	subjects := splitNonEmptyLines(subjectsRaw)
	if len(subjects) == 0 {
		return mergeCreateRefusal(result, createRefusal{
			code:    CreateRefusalNothingToPush,
			reason:  fmt.Sprintf("branch %s carries no commit ahead of %s", branch, base),
			command: "commit the change, then retry",
		}), nil
	}

	headSHA, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	result.HeadSHA = strings.TrimSpace(headSHA)

	// --set-upstream makes a plain `git push` from the worktree work right
	// after this call, instead of failing with "no upstream branch" (#609).
	// It is safe across the cases that matter here: it records the upstream
	// against whatever local branch HEAD is actually on, even when that
	// local name differs from the pushed ref; it is a no-op when an upstream
	// is already set to the same ref; and on a detached HEAD it pushes fine
	// and simply sets nothing (Git only warns, and only via the message
	// this command's own output already carries) rather than failing.
	if _, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "push", "--set-upstream", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return result, fmt.Errorf("push %s: %w", branch, err)
	}

	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = pullRequestCreateTitle(subjects)
	}
	result.Title = title

	body, err := pullRequestCreateBody(options, worktree, subjects)
	if err != nil {
		return result, err
	}

	url, adopted, err := openOrAdoptPullRequest(ctx, worktree, repository, branch, base, title, body, options.Draft,
		Options{Timeout: options.Timeout, Retry: options.Retry}, options.Closes)
	if err != nil {
		var mismatch *pullRequestBaseMismatchError
		if errors.As(err, &mismatch) {
			return mergeCreateRefusal(result, createRefusal{
				code:    CreateRefusalBaseMismatch,
				reason:  err.Error(),
				command: "wb pr land " + mismatch.url + ", or close it, or pass --base " + mismatch.gotBase,
			}), nil
		}
		return result, err
	}
	result.URL = url
	result.Adopted = adopted
	if number, numberErr := PullRequestNumber(url); numberErr == nil {
		if parsed, convErr := strconv.Atoi(number); convErr == nil {
			result.PullRequest = parsed
		}
	}
	numberText := strconv.Itoa(result.PullRequest)

	if task, claimID, bindErr := worktrees.RecordClaimPullRequestBinding(options.ProjectsRoot, worktree, worktrees.ClaimPullRequestBinding{
		Repository: repository, PullRequest: result.PullRequest, URL: url,
	}); bindErr == nil {
		result.Task, result.ClaimID = task, claimID
	} else {
		// A caller that cannot record the binding still has an opened pull
		// request; that is reported, never swallowed, but it does not turn a
		// successful creation into a refusal.
		result.Evidence["claim_binding_error"] = bindErr.Error()
	}

	result.Outcome = CreateSuccess
	result.NextCommand = "wb pr land " + repository + "#" + numberText

	if options.Land {
		return createPullRequestLand(ctx, options, result, repository, numberText)
	}
	if options.AutoMerge {
		return createPullRequestAutoMerge(ctx, options, result, repository, numberText, result.HeadSHA)
	}
	return result, nil
}

// createPullRequestLand lands the just-created or -adopted pull request
// in-process, through the existing, unmodified `LandPullRequest`. `--land`
// implies arming the way `wb pr land` itself does, so this never also runs
// `createPullRequestAutoMerge`: arming twice would be redundant at best and a
// race against itself at worst.
func createPullRequestLand(ctx context.Context, options PullRequestCreateOptions, result PullRequestCreateResult, repository, number string) (PullRequestCreateResult, error) {
	if options.LinkPreflight != nil {
		if err := options.LinkPreflight(repository); err != nil {
			return result, err
		}
	}
	landOptions := PullRequestLandOptions{}
	if options.LandOptions != nil {
		landOptions = *options.LandOptions
	}
	landOptions.Repository, landOptions.PullRequest = repository, number
	// `wb pr land` itself resolves its own repository-scoped event stream
	// from a repository it already has as its CLI argument; give this
	// nested call the same one, now that the repository is finally known.
	if options.EventsForRepository != nil {
		landOptions.Events, landOptions.Stream = options.EventsForRepository(repository)
	}
	landResult, landErr := LandPullRequest(ctx, landOptions)
	result.LandResult = &landResult
	if landErr != nil {
		return result, landErr
	}
	switch landResult.Outcome {
	case LandSuccess:
		result.Outcome = CreateSuccess
	case LandRefused:
		result.Outcome = CreateRefused
		result.RefusalCode = landResult.RefusalCode
	case LandLandedIncomplete:
		result.Outcome = CreateLandedIncomplete
		result.RefusalCode = landResult.RefusalCode
	default:
		result.Outcome = CreateFindings
	}
	result.Reason = landResult.Reason
	result.Mechanical = landResult.Mechanical
	result.ApprovedBy = landResult.ApprovedBy
	result.NextCommand = ""
	return result, nil
}

// createPullRequestAutoMerge arms GitHub auto-merge for a just-created or
// -adopted pull request, under the same authority `wb pr land` requires: a
// mechanical diff needs no approval, a non-mechanical one is refused without
// --approved-by, and arming is skipped (with the pull request left open) on
// whatever guard `autoMergeBypassesAGuard` names. It reads the pull request,
// acquires and releases the same landing-lane guard `wb pr land` acquires
// around its own arming, and runs the same draft/locked/not-mergeable
// preflight `landPreflightRefusal` runs — arming auto-merge on a pull
// request GitHub itself would refuse to merge is not a lesser action than
// landing it, and deserves the same authority.
func createPullRequestAutoMerge(ctx context.Context, options PullRequestCreateOptions, result PullRequestCreateResult, repository, number, pushedHead string) (PullRequestCreateResult, error) {
	view, viewErr := ReadPullRequest(ctx, repository, number)
	if viewErr != nil {
		return result, fmt.Errorf("read pull request %s#%s: %w", repository, number, viewErr)
	}

	// The same lane a live `wb pr land` session already drives for this
	// (repository, target) must be refused before this call spends any more
	// GitHub calls arming onto a target it does not own landing onto.
	laneRecord, laneErr := acquireLandingLane(options.ProjectsRoot, repository, view.Base.Ref, options.Lane)
	if laneErr != nil {
		var conflict *landinglane.ConflictError
		if errors.As(laneErr, &conflict) {
			return mergeCreateRefusal(result, createRefusal{
				code:    LandRefusalLandingLaneHeld,
				reason:  laneErr.Error(),
				command: "wb session recall " + conflict.Record.Owner.WBSessionID,
			}), nil
		}
		return result, laneErr
	}
	if laneRecord.Owner.WBSessionID != "" {
		defer func() {
			_ = releaseLandingLane(options.ProjectsRoot, repository, view.Base.Ref, laneRecord.Owner.WBSessionID)
		}()
	}

	if refusal := landPreflightRefusal(view, repository, number); refusal != nil {
		return mergeCreateRefusal(result, createRefusal{code: refusal.code, reason: refusal.reason, command: refusal.command}), nil
	}

	// Classify and arm against the SAME head this call just pushed: GitHub's
	// own read-after-write for a pull request it just adopted or created can
	// lag the push that produced pushedHead, and arming (or classifying) a
	// stale head would silently describe or merge different content than
	// what was actually pushed.
	view, err := pinPullRequestViewToHead(ctx, repository, number, pushedHead, view, time.Sleep)
	if err != nil {
		return result, err
	}

	files, filesErr := pullRequestChangedFiles(ctx, repository, number)
	if filesErr != nil {
		return result, fmt.Errorf("read changed files of %s#%s: %w", repository, number, filesErr)
	}
	verdict := mechanicalchange.ClassifyMechanical(files)
	result.Mechanical = verdict.Mechanical
	if !verdict.Mechanical {
		result.Evidence["not_mechanical_because"] = verdict.Summary()
	}
	result.ApprovedBy = strings.TrimSpace(options.ApprovedBy)
	// Round 3, MAJOR fix: route --approved-by through the same classifier
	// `wb pr land` uses, rather than accepting any non-empty string. Without
	// this, `wb pr create --auto-merge --approved-by ci` armed on the
	// literal "ci" (not implemented anywhere), and the reviewer-identity
	// form (which needs a posted comment `--land` implements but bare
	// --auto-merge never runs) armed with no review recorded anywhere.
	// hasReviewComment is always false here: --review-comment/
	// --review-comment-file are refused at the CLI layer for --auto-merge
	// without --land (cmd/wb/pr_create.go), since they would otherwise be
	// silently ignored by this path.
	if !result.Mechanical {
		switch classifyApprovedBy(result.ApprovedBy, false) {
		case approvalKindEmpty:
			return mergeCreateRefusal(result, createRefusal{
				code:    CreateRefusalUnapprovedPatch,
				reason:  fmt.Sprintf("pull request %s#%s exists but is not a mechanical change, so --auto-merge needs a recorded review", repository, number),
				command: "wb pr create --auto-merge --approved-by <review-file-or-comment-url>",
			}), nil
		case approvalKindCI:
			return mergeCreateRefusal(result, createRefusal{
				code:    CreateRefusalUnapprovedPatch,
				reason:  "--approved-by ci is not implemented yet; follow-up: https://github.com/sneat-dev/wb/issues/619",
				command: "wb pr create --auto-merge --approved-by <review-file-or-comment-url>",
			}), nil
		case approvalKindIdentity:
			return mergeCreateRefusal(result, createRefusal{
				code: CreateRefusalIdentityNeedsLand,
				reason: "the reviewer-identity form of --approved-by needs --land to post the review comment; " +
					"--auto-merge alone never posts it, so it would arm with no review recorded anywhere",
				command: "wb pr create --land --approved-by " + result.ApprovedBy + " --review-comment \"<the review>\"",
			}), nil
		case approvalKindFile, approvalKindURL:
			// Unchanged: passed through verbatim, exactly as before this fix.
		}
	}
	if options.LinkPreflight != nil {
		if err := options.LinkPreflight(repository); err != nil {
			return result, err
		}
	}
	landOptions := PullRequestLandOptions{Repository: repository, AllowUnfenced: options.AllowUnfenced}
	if reason := autoMergeBypassesAGuard(ctx, landOptions, result.BaseRef); reason != "" {
		result.AutoMergeReason = reason
		result.Outcome = CreateFindings
		result.Reason = "not armed: " + reason
		return result, nil
	}
	commits, commitsErr := pullRequestCommits(ctx, repository, number)
	if commitsErr != nil {
		return result, fmt.Errorf("read commits of %s#%s: %w", repository, number, commitsErr)
	}
	subject := fmt.Sprintf("%s (#%s)", view.Title, number)
	body := aggregatedCommitMessage(view, commits, result.ApprovedBy, "")
	method := strings.TrimSpace(options.MergeMethod)
	if method == "" {
		method = "merge"
	}
	if reason := enablePullRequestAutoMerge(ctx, repository, number, method, view.Head.SHA, subject, body); reason != "" {
		return result, fmt.Errorf("arm auto-merge for %s#%s: %s", repository, number, reason)
	}
	result.AutoMergeArmed = true
	result.NextCommand = "wb wait pr " + repository + "#" + number + " --until closed"
	return result, nil
}

// pinPullRequestViewPollDelay is the real poll interval pinPullRequestViewToHead
// waits between re-reads.
const pinPullRequestViewPollDelay = 200 * time.Millisecond

// pinPullRequestViewToHead re-reads a pull request until its own reported
// head SHA matches pushedHead, bounded rather than immediate: GitHub's own
// read-after-write for a pull request this call just adopted or created can
// briefly still report the head observed before the push that produced
// pushedHead. A view already at pushedHead is returned unchanged with no
// extra call.
//
// sleep is the retry-backoff seam: createPullRequestAutoMerge always passes
// time.Sleep; a test passes a recorder. It is a function parameter, not a
// package-level mutable var, so a test cannot leave shared package state
// mutated for another test running in parallel.
func pinPullRequestViewToHead(ctx context.Context, repository, number, pushedHead string, view PullRequestView, sleep func(time.Duration)) (PullRequestView, error) {
	if pushedHead == "" || view.Head.SHA == pushedHead {
		return view, nil
	}
	const attempts = 5
	for attempt := 1; attempt < attempts; attempt++ {
		sleep(pinPullRequestViewPollDelay)
		refreshed, err := ReadPullRequest(ctx, repository, number)
		if err != nil {
			return view, fmt.Errorf("re-read pull request %s#%s to confirm its pushed head: %w", repository, number, err)
		}
		view = refreshed
		if view.Head.SHA == pushedHead {
			return view, nil
		}
	}
	return view, fmt.Errorf("pull request %s#%s still reports head %s, not the pushed head %s",
		repository, number, view.Head.SHA, pushedHead)
}

// openOrAdoptPullRequest opens or adopts one branch's pull request. It is a
// thin extension of the shared, idempotent `openPullRequest` (engine.go):
// that primitive is reused verbatim for the non-draft path, which is the
// common case. Draft creation is a separate branch because `openPullRequest`
// deliberately never passes `--draft` — its only caller before this one is
// `wb worktree merge`'s non-draft candidate publication — and changing its
// signature would also have to change that call site, which belongs to a
// change in flight elsewhere. The adoption check therefore runs once here so
// both branches agree on it, and `openPullRequest` repeats its own equivalent
// check before ever creating anything, so no path can create twice.
// pullRequestBaseMismatchError reports that an already-open pull request was
// found for the branch, but against a different base than this invocation
// asked for. Adoption looks for the branch's open pull request against ANY
// base — GitHub allows exactly one open pull request per (repository, head
// branch) regardless of base, so a `--base`-scoped list can miss the one
// that already exists — but adopting a pull request onto the WRONG base
// would silently retarget it, so a mismatch refuses instead.
type pullRequestBaseMismatchError struct {
	url, wantBase, gotBase string
}

func (mismatch *pullRequestBaseMismatchError) Error() string {
	return fmt.Sprintf("pull request %s is already open against %s, not %s", mismatch.url, mismatch.gotBase, mismatch.wantBase)
}

// openOrAdoptPullRequest opens or adopts one branch's pull request, pinned to
// repository with `--repo` on every `gh pr list`/`gh pr create` call so the
// worktree's own cwd-inferred repository is never silently substituted.
func openOrAdoptPullRequest(ctx context.Context, worktree, repository, branch, base, title, body string, draft bool, options Options, closesIssues []int) (url string, adopted bool, err error) {
	// Round 3, minor 6: the body field is read here too, only so an
	// adopted (already-open) pull request's --closes lines can be applied
	// to it below — the "body" this function otherwise takes as a
	// parameter is used solely by the create calls further down, and was
	// never previously applied to a pull request that already existed.
	existing, listErr := githubRead(ctx, worktree, "pr", "list", "--repo", repository, "--head", branch,
		"--state", "open", "--json", "url,baseRefName,body", "--jq", ".[0] | (.url + \"\\t\" + .baseRefName + \"\\t\" + (.body // \"\"))")
	if listErr == nil {
		if trimmed := strings.TrimSpace(existing); trimmed != "" {
			parts := strings.SplitN(trimmed, "\t", 3)
			if len(parts) >= 2 {
				url, gotBase := parts[0], parts[1]
				currentBody := ""
				if len(parts) == 3 {
					currentBody = parts[2]
				}
				if gotBase != base {
					return "", false, &pullRequestBaseMismatchError{url: url, wantBase: base, gotBase: gotBase}
				}
				if len(closesIssues) > 0 {
					if editErr := applyClosesToAdoptedPullRequest(ctx, worktree, repository, url, currentBody, closesIssues, options); editErr != nil {
						return "", false, editErr
					}
				}
				return url, true, nil
			}
		}
	}
	if !draft {
		created, _, createErr := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "gh", "pr", "create",
			"--repo", repository, "--base", base, "--head", branch, "--title", title, "--body", body)
		if createErr != nil {
			return "", false, createErr
		}
		if createdURL := lastNonEmptyLine(created); createdURL != "" {
			return createdURL, false, nil
		}
		return "", false, fmt.Errorf("gh pr create returned no pull request URL")
	}
	draftBody := body
	if manifest, manifestErr := worktrees.ReadManifest(worktree); manifestErr == nil {
		draftBody = prmeta.Append(draftBody, prmeta.Provenance{Effort: manifest.EffortID})
	}
	created, _, createErr := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "gh", "pr", "create",
		"--repo", repository, "--base", base, "--head", branch, "--title", title, "--body", draftBody, "--draft")
	if createErr != nil {
		return "", false, createErr
	}
	if createdURL := lastNonEmptyLine(created); createdURL != "" {
		return createdURL, false, nil
	}
	return "", false, fmt.Errorf("gh pr create --draft returned no pull request URL")
}

// resolvePullRequestCreateWorktree resolves the CLI's `<worktree|task>`
// argument. An existing directory is used as-is; anything else is looked up
// as a task name against the fleet's worktree inventory. Resolution never
// runs a network fetch: the guard and the base-branch fetch that follow are
// where a caller pays that cost, once, for the worktree it actually meant.
// ResolvePullRequestCreateWorktree exports resolvePullRequestCreateWorktree
// for cmd/wb's best-effort #615 prompt-suggestion lookup, which needs the
// same worktree-path-or-task-name resolution `wb pr create` itself uses but
// runs before CreatePullRequest is called.
func ResolvePullRequestCreateWorktree(ctx context.Context, projectsRoot, argument string) (string, error) {
	return resolvePullRequestCreateWorktree(ctx, projectsRoot, argument)
}

func resolvePullRequestCreateWorktree(ctx context.Context, projectsRoot, argument string) (string, error) {
	if info, statErr := os.Stat(argument); statErr == nil && info.IsDir() {
		// Absolute: ReadManifest (and the secure directory helpers it uses)
		// refuse a relative path outright, and "." is the CLI's own default.
		absolute, absErr := filepath.Abs(argument)
		if absErr != nil {
			return "", fmt.Errorf("resolve %s to an absolute path: %w", argument, absErr)
		}
		return absolute, nil
	}
	entries, err := worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: projectsRoot, Task: argument})
	if err != nil {
		return "", fmt.Errorf("resolve task %q: %w", argument, err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no worktree or task %q found", argument)
	}
	if len(entries) > 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Repository+" "+entry.WorktreeDir)
		}
		return "", fmt.Errorf("task %q has more than one worktree (%s); pass a path instead", argument, strings.Join(names, "; "))
	}
	return entries[0].WorktreeDir, nil
}

// worktreeStatusEntry is one entry of `git status --porcelain=v1 -z`: the
// index and worktree status columns, kept exactly as Git wrote them (a blank
// column is a space), and the path with no quoting. A rename or copy carries
// its new path here; its old path is dropped.
type worktreeStatusEntry struct {
	index    byte
	worktree byte
	path     string
}

// String renders the entry for a human, "<status> <path>", without the blank
// column padding.
func (entry worktreeStatusEntry) String() string {
	return strings.TrimSpace(string([]byte{entry.index, entry.worktree})) + " " + entry.path
}

// parsePorcelainStatusZ reads `git status --porcelain=v1 -z` output by its
// fixed columns: two status bytes, one space, then the path up to the next NUL.
// A rename or copy ("R"/"C" in either column) is followed by one more NUL
// field holding the original path, which is skipped. Nothing is trimmed: the
// first status column is blank for an unstaged change, and a path may begin
// with a dot or a space.
//
// It fails closed: any non-empty field that is not "XY path", or a rename with
// no original path, is an error, never a skipped entry. A status the caller
// cannot fully read must refuse the operation rather than look like a clean
// tree. An unrecognised XY code is still an entry, so it counts as dirty.
func parsePorcelainStatusZ(output string) ([]worktreeStatusEntry, error) {
	var entries []worktreeStatusEntry
	fields := strings.Split(output, "\x00")
	for position := 0; position < len(fields); position++ {
		field := fields[position]
		if field == "" {
			continue
		}
		if len(field) < 4 || field[2] != ' ' {
			return nil, fmt.Errorf("unparseable git status entry %q", field)
		}
		entry := worktreeStatusEntry{index: field[0], worktree: field[1], path: field[3:]}
		if strings.ContainsRune("RC", rune(entry.index)) || strings.ContainsRune("RC", rune(entry.worktree)) {
			position++ // the original path of a rename or copy
			if position >= len(fields) || fields[position] == "" {
				return nil, fmt.Errorf("git status rename entry %q has no original path", field)
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// readPorcelainStatus runs `git status --porcelain=v1 -z` and parses stdout
// alone. Stderr is kept apart so a git warning cannot fuse with the first
// entry, and anything on it, even with exit 0, is an error: a status git only
// partly read (an unreadable directory) must not pass as clean.
func readPorcelainStatus(ctx context.Context, run runner.Runner, timeout time.Duration, worktree string) ([]worktreeStatusEntry, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	result, err := run.RunOpts(ctx, worktree, runner.RunOptions{Env: console.Env()}, "git", "status", "--porcelain=v1", "-z")
	if err != nil {
		return nil, fmt.Errorf("git status in %s: %w: %s", worktree, err, strings.TrimSpace(result.Stderr))
	}
	if diagnostic := strings.TrimSpace(result.Stderr); diagnostic != "" {
		return nil, fmt.Errorf("git status in %s reported: %s", worktree, diagnostic)
	}
	return parsePorcelainStatusZ(result.Stdout)
}

// describeStatusEntries renders entries for a refusal message.
func describeStatusEntries(entries []worktreeStatusEntry) []string {
	described := make([]string, 0, len(entries))
	for _, entry := range entries {
		described = append(described, entry.String())
	}
	return described
}

func splitNonEmptyLines(value string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// wipSubjectPattern recognizes a work-in-progress commit subject in any of
// its ordinary spellings: "wip", "wip:", "wip(scope):". GitHub would
// otherwise happily title a pull request with exactly this, and a caller
// cannot fix that afterward without rewriting a protected branch's history.
var wipSubjectPattern = regexp.MustCompile(`(?i)^wip\b`)

func isWipSubject(subject string) bool {
	return wipSubjectPattern.MatchString(strings.TrimSpace(subject))
}

// pullRequestCreateTitle derives a pull request title from the branch's own
// commit subjects: a single commit contributes its subject verbatim, and
// several commits contribute the most recent one that reads like a real
// change. A "wip"-shaped subject never wins either way, because it is a
// placeholder, not a description.
func pullRequestCreateTitle(subjects []string) string {
	nonEmpty := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		if trimmed := strings.TrimSpace(subject); trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	if len(nonEmpty) == 0 {
		return "Open pull request"
	}
	if len(nonEmpty) == 1 {
		if !isWipSubject(nonEmpty[0]) {
			return nonEmpty[0]
		}
		return "apply 1 commit"
	}
	usable := make([]string, 0, len(nonEmpty))
	for _, subject := range nonEmpty {
		if !isWipSubject(subject) {
			usable = append(usable, subject)
		}
	}
	if len(usable) == 0 {
		return fmt.Sprintf("apply %d commits", len(nonEmpty))
	}
	if len(usable) == 1 {
		return usable[0]
	}
	// git log lists newest first; the oldest usable subject normally states
	// the branch's own purpose, and later commits are review/CI repairs.
	base := usable[len(usable)-1]
	related := len(usable) - 1
	word := "changes"
	if related == 1 {
		word = "change"
	}
	return fmt.Sprintf("%s and %d related %s", base, related, word)
}

// pullRequestCreateBody derives a pull request body from options and the
// branch's own commits, in the order the contract fixes: literal --body wins,
// then --body-file, then the commit-derived default. --title alone never
// substitutes for a body: overriding what the pull request is called says
// nothing about what it contains.
func pullRequestCreateBody(options PullRequestCreateOptions, worktree string, subjects []string) (string, error) {
	body, err := pullRequestCreateBodyWithoutCloses(options, worktree, subjects)
	if err != nil {
		return "", err
	}
	return withClosesPrefix(body, options.Closes), nil
}

func pullRequestCreateBodyWithoutCloses(options PullRequestCreateOptions, worktree string, subjects []string) (string, error) {
	if body := strings.TrimSpace(options.Body); body != "" {
		return options.Body, nil
	}
	if path := strings.TrimSpace(options.BodyFile); path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read --body-file %s: %w", path, err)
		}
		return string(contents), nil
	}
	if len(subjects) == 1 {
		commitBody, _, err := runCommand(context.Background(), options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "log", "-1", "--format=%b")
		if err == nil {
			if trimmed := strings.TrimSpace(commitBody); trimmed != "" {
				return trimmed, nil
			}
		}
		return "", nil
	}
	var body strings.Builder
	body.WriteString("Mechanically prepared by `wb pr create` from the branch's own commits.\n\nCommits:\n\n")
	for index := len(subjects) - 1; index >= 0; index-- {
		fmt.Fprintf(&body, "- %s\n", subjects[index])
	}
	return body.String(), nil
}

func appendCreateEvent(events streams.EventAppender, streamName string, result PullRequestCreateResult, started time.Time, createErr error) {
	if events == nil {
		return
	}
	outcome := string(result.Outcome)
	if createErr != nil || outcome == "" {
		outcome = string(CreateFindings)
	}
	evidence := map[string]string{
		"mechanical": strconv.FormatBool(result.Mechanical),
		"adopted":    strconv.FormatBool(result.Adopted),
		"auto_merge": strconv.FormatBool(result.AutoMergeArmed),
	}
	if result.Task != "" {
		evidence["task"] = result.Task
	}
	if result.ApprovedBy != "" {
		evidence["approved_by"] = result.ApprovedBy
	}
	detail := result.Reason
	if createErr != nil {
		detail = createErr.Error()
	}
	_ = events.Append(streams.Event{
		Stream:      streamName,
		Verb:        "pr create",
		Repository:  result.Repository,
		Outcome:     outcome,
		RefusalCode: result.RefusalCode,
		Detail:      detail,
		DurationMS:  time.Since(started).Milliseconds(),
		Evidence:    evidence,
	})
}
