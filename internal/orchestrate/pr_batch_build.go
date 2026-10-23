package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// BatchStatePending marks a step recorded but not yet merged.
const BatchStatePending = "pending"

// BatchOptions configures every `wb pr batch` verb.
type BatchOptions struct {
	Repository   string
	Target       string
	Worktree     string
	Pins         []string
	ProjectsRoot string
	Timeout      time.Duration
	// Overlap adds, to the delta, each source's own diff of every file more
	// than one source changed: the cheap place to look for a semantic
	// conflict that produced no textual one.
	Overlap bool
	GitHub  BatchGitHub

	run         runner.Runner
	checkout    func(worktree string) (repository, branch, base string, err error)
	laneSession func(projectsRoot, repository, branch string) string
	operator    func() ReviewerIdentity
	cache       BatchCache
}

func (options BatchOptions) resolveRunner() runner.Runner {
	if options.run != nil {
		return options.run
	}
	return defaultRunner
}

func (options BatchOptions) resolveGitHub() BatchGitHub {
	if options.GitHub != nil {
		return options.GitHub
	}
	return DefaultBatchGitHub()
}

func (options BatchOptions) resolveCheckout() func(string) (string, string, string, error) {
	if options.checkout != nil {
		return options.checkout
	}
	return manifestCheckout
}

func manifestCheckout(worktree string) (string, string, string, error) {
	manifest, err := worktrees.ReadManifest(worktree)
	return manifest.Repository, manifest.Branch, manifest.Base, err
}

func (options BatchOptions) resolveLaneSession() func(string, string, string) string {
	if options.laneSession != nil {
		return options.laneSession
	}
	return claimSessionForBranch
}

// claimSessionForBranch returns the WB session of this machine's active Work
// Log claim on branch, or "" when none is readable here.
func claimSessionForBranch(projectsRoot, repository, branch string) string {
	claims, _ := worktrees.ListActiveClaimSummaries(projectsRoot, "")
	return claimSessionIn(claims, repository, branch)
}

func claimSessionIn(claims []worktrees.ActiveClaimSummary, repository, branch string) string {
	for _, claim := range claims {
		if strings.EqualFold(claim.Repository, repository) && claim.Branch == branch {
			return claim.WBSessionID
		}
	}
	return ""
}

func (options BatchOptions) resolveOperator() ReviewerIdentity {
	if options.operator != nil {
		return options.operator()
	}
	return currentSessionIdentity()
}

func (options BatchOptions) resolveCache() BatchCache {
	if options.cache != nil {
		return options.cache
	}
	return fileBatchCache{home: func() (string, error) { return wbhome.Root(options.ProjectsRoot) }}
}

func (options BatchOptions) git() batchGit {
	return batchGit{run: options.resolveRunner(), dir: options.Worktree, timeout: options.Timeout}
}

// BatchCache is WB's private, append-only copy of batch state.
type BatchCache interface {
	Load(repository, branch string) (BatchState, bool, error)
	Save(state BatchState) error
}

// fileBatchCache keeps one immutable file per revision under the WB home.
type fileBatchCache struct{ home func() (string, error) }

func (cache fileBatchCache) dir(repository, branch string) (string, error) {
	home, err := cache.home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "reports", "pr-batch", strings.ReplaceAll(repository, "/", "__"), branch), nil
}

func (cache fileBatchCache) Load(repository, branch string) (BatchState, bool, error) {
	dir, err := cache.dir(repository, branch)
	if err != nil {
		return BatchState{}, false, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return BatchState{}, false, nil
	}
	if err != nil {
		return BatchState{}, false, err
	}
	latest := -1
	for _, entry := range entries {
		if revision, convErr := strconv.Atoi(strings.TrimSuffix(entry.Name(), ".json")); convErr == nil && revision > latest {
			latest = revision
		}
	}
	if latest < 0 {
		return BatchState{}, false, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(latest)+".json"))
	if err != nil {
		return BatchState{}, false, err
	}
	var state BatchState
	if err := json.Unmarshal(raw, &state); err != nil {
		return BatchState{}, false, fmt.Errorf("decode batch cache revision %d: %w", latest, err)
	}
	return state, true, nil
}

func (cache fileBatchCache) Save(state BatchState) error {
	dir, err := cache.dir(state.Repository, state.Branch)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(state, "", "  ")
	// O_EXCL keeps every revision immutable: a second writer of the same
	// revision fails instead of rewriting history.
	file, err := os.OpenFile(filepath.Join(dir, strconv.Itoa(state.Revision)+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("record batch revision %d: %w", state.Revision, err)
	}
	_, err = file.Write(raw)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// save writes the next revision of state.
func (options BatchOptions) save(state *BatchState) error {
	cache := options.resolveCache()
	// State rebuilt from origin can trail this machine's cache; continue the
	// cache's revision sequence rather than collide with it.
	if latest, found, err := cache.Load(state.Repository, state.Branch); err != nil {
		return err
	} else if found && latest.Revision > state.Revision {
		state.Revision = latest.Revision
	}
	state.Revision++
	return cache.Save(*state)
}

var reviewHeaderPattern = regexp.MustCompile(`(?m)^\*\*Review by ([^*\s]+)\*\*`)

// declaredReviewer returns the identity a review comment declares in its
// `**Review by model@harness@session**` header.
func declaredReviewer(body string) ReviewerIdentity {
	match := reviewHeaderPattern.FindStringSubmatch(body)
	if match == nil {
		return ReviewerIdentity{}
	}
	return ParseReviewerIdentity(match[1])
}

func identityComplete(identity ReviewerIdentity) bool {
	for _, part := range []string{identity.Model, identity.Harness, identity.Session} {
		if part == "" || strings.EqualFold(part, unknownIdentityPart) {
			return false
		}
	}
	return true
}

func batchBranchPattern(target string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(target) + `-batch-[0-9]+$`)
}

// checkBatchWorktree verifies the batch runs in a clean WB worktree of the
// repository, cut from the target, on a <target>-batch-<n> branch.
func (options BatchOptions) checkBatchWorktree(ctx context.Context, git batchGit) (string, *BatchIssue) {
	create := fmt.Sprintf("wb worktree create %s-batch-<n> %s --base %s --branch %s-batch-<n> --model <model> --original-prompt-file -", options.Target, options.Repository, options.Target, options.Target)
	refuse := func(reason string) (string, *BatchIssue) {
		return "", &BatchIssue{Code: BatchRefusalWorktree, Reason: reason + "; create one with: " + create}
	}
	if strings.TrimSpace(options.Worktree) == "" {
		return refuse("no batch worktree given")
	}
	repository, branch, base, err := options.resolveCheckout()(options.Worktree)
	switch {
	case err != nil:
		return refuse(fmt.Sprintf("%s is not a WB-managed worktree: %v", options.Worktree, err))
	case !strings.EqualFold(repository, options.Repository):
		return refuse(fmt.Sprintf("%s is a worktree of %s, not %s", options.Worktree, repository, options.Repository))
	case strings.TrimPrefix(base, "origin/") != options.Target:
		return refuse(fmt.Sprintf("%s was cut from %s, not %s", options.Worktree, base, options.Target))
	case !batchBranchPattern(options.Target).MatchString(branch):
		return refuse(fmt.Sprintf("branch %s is not %s-batch-<n>", branch, options.Target))
	}
	status, err := git.out(ctx, "status", "--porcelain")
	if err != nil {
		return refuse(err.Error())
	}
	if status != "" {
		return refuse(fmt.Sprintf("%s has uncommitted changes", options.Worktree))
	}
	return branch, nil
}

// verifyPins applies REQ batch-source-pins to every pin and returns the
// source steps, or every refusal together.
func (options BatchOptions) verifyPins(ctx context.Context, git batchGit, pins []BatchPin, batchHead, targetRef string) ([]BatchStep, []BatchIssue, error) {
	github := options.resolveGitHub()
	operator := options.resolveOperator()
	var steps []BatchStep
	var issues []BatchIssue
	for _, pin := range pins {
		label := "#" + strconv.Itoa(pin.Number)
		issue := func(code, reason string) {
			issues = append(issues, BatchIssue{Code: code, Source: label, Reason: reason})
		}
		view, err := github.PullRequest(ctx, options.Repository, pin.Number)
		if err != nil {
			return nil, nil, err
		}
		if !strings.EqualFold(view.State, "open") || view.Draft || view.Merged {
			issue(BatchRefusalSourceNotOpen, fmt.Sprintf("%s is %s (draft: %t)", label, view.State, view.Draft))
			continue
		}
		if view.Base.Ref != options.Target {
			issue(BatchRefusalSourceWrongBase, fmt.Sprintf("%s targets %s, not %s", label, view.Base.Ref, options.Target))
			continue
		}
		head := strings.ToLower(view.Head.SHA)
		if !strings.HasPrefix(head, pin.SHA) {
			issue(BatchRefusalSourceHeadMoved, fmt.Sprintf("%s head is %s, not the pinned %s", label, shortMergeRevision(head), pin.SHA))
			continue
		}
		if _, err := git.out(ctx, "fetch", "--quiet", "origin", head); err != nil {
			return nil, nil, err
		}
		if landed, err := git.test(ctx, "merge-base", "--is-ancestor", head, targetRef); err != nil {
			return nil, nil, err
		} else if landed {
			issue(BatchRefusalSourceAlreadyLanded, fmt.Sprintf("%s head %s is already in %s", label, shortMergeRevision(head), options.Target))
			continue
		}
		if included, err := git.test(ctx, "merge-base", "--is-ancestor", head, batchHead); err != nil {
			return nil, nil, err
		} else if included {
			issue(BatchRefusalSourceAlreadyIncluded, fmt.Sprintf("%s head %s is already in the batch", label, shortMergeRevision(head)))
			continue
		}
		comment, err := github.IssueComment(ctx, options.Repository, pin.ReviewCommentID)
		if err != nil {
			issue(BatchRefusalSourceUnreviewed, err.Error())
			continue
		}
		if comment.PullRequest != pin.Number {
			issue(BatchRefusalSourceUnreviewed, fmt.Sprintf("review comment %d is on #%d, not %s", pin.ReviewCommentID, comment.PullRequest, label))
			continue
		}
		body := comment.Body
		if reviewed, parseErr := parseReviewedHeadLine(body); parseErr != nil || reviewed != head {
			issue(BatchRefusalSourceUnreviewed, fmt.Sprintf("review %s does not end Reviewed-Head: %s", pin.ReviewURL, head))
			continue
		}
		reviewer := declaredReviewer(body)
		lane := options.resolveLaneSession()(options.ProjectsRoot, options.Repository, view.Head.Ref)
		// Independence is judged on the session alone: the same session
		// declaring another model name is still the same author.
		if !identityComplete(reviewer) || (lane != "" && strings.EqualFold(reviewer.Session, lane)) || strings.EqualFold(reviewer.Session, operator.Session) {
			issue(BatchRefusalSourceSelfReview, fmt.Sprintf("review identity %q is incomplete, the lane author's, or the operator's", reviewer.String()))
			continue
		}
		steps = append(steps, BatchStep{
			Kind: BatchStepSource, PullRequest: pin.Number, Head: head,
			ReviewCommentID: pin.ReviewCommentID, ReviewDigest: ReviewDigest(body),
			Reviewer: reviewer.String(), Identity: "declared", State: BatchStatePending,
		})
	}
	// A pin under another pin would merge as "Already up to date" and get no
	// merge commit of its own.
	for i, step := range steps {
		for j, other := range steps {
			if i == j {
				continue
			}
			if under, err := git.test(ctx, "merge-base", "--is-ancestor", step.Head, other.Head); err != nil {
				return nil, nil, err
			} else if under {
				issues = append(issues, BatchIssue{Code: BatchRefusalSourceAlreadyIncluded, Source: "#" + strconv.Itoa(step.PullRequest),
					Reason: fmt.Sprintf("#%d head is contained in #%d", step.PullRequest, other.PullRequest)})
			}
		}
	}
	return steps, issues, nil
}

func (options BatchOptions) parsePins() ([]BatchPin, *BatchIssue) {
	pins := make([]BatchPin, 0, len(options.Pins))
	for _, raw := range options.Pins {
		pin, err := ParseBatchPin(options.Repository, raw)
		if err != nil {
			return nil, &BatchIssue{Code: BatchRefusalPinInvalid, Reason: err.Error()}
		}
		pins = append(pins, pin)
	}
	return pins, nil
}

// checkMergeRoute applies REQ batch-refuses-non-merge-targets.
func (options BatchOptions) checkMergeRoute(ctx context.Context) (*BatchIssue, error) {
	github := options.resolveGitHub()
	allowed, err := github.AllowsMergeCommits(ctx, options.Repository)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return &BatchIssue{Code: BatchRefusalMergeCommitsDisabled, Reason: options.Repository + " does not allow merge commits; a batch keeps reviewed heads only through merge commits"}, nil
	}
	queued, err := github.UsesMergeQueue(ctx, options.Repository, options.Target)
	if err != nil {
		return nil, err
	}
	if queued {
		return &BatchIssue{Code: BatchRefusalTargetMergeQueue, Reason: options.Target + " merges through a merge queue, which rebuilds the reviewed heads"}, nil
	}
	return nil, nil
}

// verbCommand is the exact command line for another batch verb on the same
// batch.
func (options BatchOptions) verbCommand(verb string) string {
	return fmt.Sprintf("wb pr batch %s %s --target %s --worktree %s", verb, options.Repository, options.Target, shellSingleQuote(options.Worktree))
}

func refuseIssue(result BatchResult, issue BatchIssue) BatchResult {
	return result.refuse(issue.Code, issue.Reason, "")
}

// CreatePullRequestBatch is `wb pr batch create`.
func CreatePullRequestBatch(ctx context.Context, options BatchOptions) (BatchResult, error) {
	result := BatchResult{SchemaVersion: 1, Verb: "pr batch create", Worktree: options.Worktree}
	pins, issue := options.parsePins()
	if issue == nil && len(pins) == 0 {
		issue = &BatchIssue{Code: BatchRefusalPinInvalid, Reason: "name at least one <number>@<sha>=<review-comment-url> pin"}
	}
	if issue != nil {
		return refuseIssue(result, *issue), nil
	}
	if issue, err := options.checkMergeRoute(ctx); err != nil || issue != nil {
		if err != nil {
			return result, err
		}
		return refuseIssue(result, *issue), nil
	}
	git := options.git()
	branch, issue := options.checkBatchWorktree(ctx, git)
	if issue != nil {
		return refuseIssue(result, *issue), nil
	}
	cached, found, err := options.resolveCache().Load(options.Repository, branch)
	if err != nil {
		return result, err
	}
	if found && len(cached.Steps) > 0 {
		if !samePins(cached, pins) || cached.conflicted() >= 0 {
			return result.refuse(BatchRefusalWorktree, "batch "+branch+" already has sources; resume it, or start another batch", options.verbCommand("resume")), nil
		}
		// An identical rerun finishes publication and changes nothing else;
		// it never pushes commits the batch did not record.
		head, err := git.out(ctx, "rev-parse", "HEAD")
		if err != nil {
			return result, err
		}
		if last := cached.Steps[len(cached.Steps)-1].MergeCommit; last != "" && head != last {
			return result.refuse(BatchRefusalWorktree, fmt.Sprintf("batch worktree is at %s, past the recorded batch head %s", shortMergeRevision(head), shortMergeRevision(last)), ""), nil
		}
		return options.continueBatch(ctx, git, cached, result)
	}
	if _, err := git.out(ctx, "fetch", "--quiet", "origin", options.Target); err != nil {
		return result, err
	}
	tip, err := git.out(ctx, "rev-parse", "origin/"+options.Target)
	if err != nil {
		return result, err
	}
	head, err := git.out(ctx, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	if head != tip {
		return result.refuse(BatchRefusalWorktree, fmt.Sprintf("batch worktree is at %s, not origin/%s %s", shortMergeRevision(head), options.Target, shortMergeRevision(tip)), ""), nil
	}
	steps, issues, err := options.verifyPins(ctx, git, pins, head, "origin/"+options.Target)
	if err != nil {
		return result, err
	}
	if len(issues) > 0 {
		result.Refusals = issues
		return result.refuse(issues[0].Code, fmt.Sprintf("%d pin(s) refused; nothing was created", len(issues)), ""), nil
	}
	state := BatchState{
		Version: 1, Repository: options.Repository, Target: options.Target, Branch: branch, Base: tip,
		MergeOptions: append([]string(nil), batchMergeOptions...), Steps: steps,
	}
	state.addBuilder(options.resolveOperator().String())
	result.Changed = true
	return options.continueBatch(ctx, git, state, result)
}

func samePins(state BatchState, pins []BatchPin) bool {
	sources := state.sources()
	if len(sources) != len(pins) {
		return false
	}
	for i, pin := range pins {
		if sources[i].PullRequest != pin.Number || !strings.HasPrefix(sources[i].Head, pin.SHA) {
			return false
		}
	}
	return true
}

// continueBatch merges every pending step, then publishes the branch and its
// one pull request. It stops, saved, on the first conflict.
func (options BatchOptions) continueBatch(ctx context.Context, git batchGit, state BatchState, result BatchResult) (BatchResult, error) {
	for i := range state.Steps {
		step := &state.Steps[i]
		if step.State != BatchStatePending {
			continue
		}
		result.Changed = true
		conflicts, err := options.mergeStep(ctx, git, state, step)
		if err != nil {
			return result, err
		}
		if len(conflicts) > 0 {
			step.State, step.ConflictPaths = BatchStateConflicted, conflicts
			if err := options.save(&state); err != nil {
				return result, err
			}
			result.State = &state
			return result.finding(BatchRefusalConflict,
				fmt.Sprintf("merging %s conflicts in %s; resolve them in %s and commit the merge", stepLabel(state, *step), strings.Join(conflicts, ", "), options.Worktree),
				options.verbCommand("resume")), nil
		}
		step.State = BatchStateMerged
		if err := options.save(&state); err != nil {
			return result, err
		}
	}
	return options.publish(ctx, git, state, result)
}

func stepLabel(state BatchState, step BatchStep) string {
	if step.Kind == BatchStepSource {
		return "#" + strconv.Itoa(step.PullRequest)
	}
	return state.Target + " " + shortMergeRevision(step.Head)
}

// mergeStep makes the step's merge commit, or returns the conflicting paths
// with the merge left in progress.
func (options BatchOptions) mergeStep(ctx context.Context, git batchGit, state BatchState, step *BatchStep) ([]string, error) {
	message := fmt.Sprintf("Merge %s (%s) into %s", stepLabel(state, *step), shortMergeRevision(step.Head), state.Branch)
	if step.Kind == BatchStepSource {
		message = fmt.Sprintf("Merge #%d (%s) into %s", step.PullRequest, shortMergeRevision(step.Head), state.Branch)
	}
	if _, err := git.out(ctx, configArgs(batchMergeOptions, "merge", "--no-ff", "--no-edit", "-m", message, step.Head)...); err != nil {
		conflicts, listErr := git.out(ctx, "diff", "--name-only", "--diff-filter=U")
		if listErr != nil || conflicts == "" {
			return nil, err
		}
		return strings.Split(conflicts, "\n"), nil
	}
	merge, err := git.out(ctx, "rev-parse", "HEAD")
	step.MergeCommit = merge
	return nil, err
}

// publish pushes the batch branch without force and opens or updates its one
// pull request with a body generated from the state.
func (options BatchOptions) publish(ctx context.Context, git batchGit, state BatchState, result BatchResult) (BatchResult, error) {
	result.State = &state
	head, err := git.out(ctx, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	result.Head = head
	push, err := git.exec(ctx, "push", "--quiet", "origin", "HEAD:refs/heads/"+state.Branch)
	if err != nil {
		if strings.Contains(push.Stderr, "[rejected]") || strings.Contains(push.Stderr, "non-fast-forward") || strings.Contains(push.Stderr, "fetch first") {
			return result.refuse(BatchRefusalMoved, "origin/"+state.Branch+" moved; nothing was force-pushed", ""), nil
		}
		return result, fmt.Errorf("push %s: %w: %s", state.Branch, err, strings.TrimSpace(push.Stderr))
	}
	github := options.resolveGitHub()
	view, err := github.BatchPullRequest(ctx, options.Repository, state.Branch)
	if err != nil {
		return result, err
	}
	if view.Number != 0 && (view.Merged || !strings.EqualFold(view.State, "open")) {
		return result.refuse(BatchRefusalClosed, fmt.Sprintf("batch pull request #%d for %s is merged or closed; start a new batch", view.Number, state.Branch), ""), nil
	}
	number := view.Number
	body := RenderBatchBody(state)
	if number == 0 {
		number, err = github.CreatePullRequest(ctx, options.Repository, state.Branch, state.Target, "Batch "+state.Branch+" into "+state.Target, body)
		if err != nil {
			return result, err
		}
		result.Changed = true
	} else if result.Changed || view.Body != body {
		if err := github.UpdatePullRequestBody(ctx, options.Repository, number, body); err != nil {
			return result, err
		}
	}
	if state.PullRequest != number {
		state.PullRequest = number
		if err := options.save(&state); err != nil {
			return result, err
		}
	}
	result.State = &state
	result.PullRequest = options.Repository + "#" + strconv.Itoa(number)
	result.Outcome = LandSuccess
	result.Reason = fmt.Sprintf("%d step(s) on %s at %s", len(state.Steps), state.Branch, shortMergeRevision(head))
	return result, nil
}

// ResumePullRequestBatch is `wb pr batch resume`: it accepts a committed
// conflict resolution and continues the batch.
func ResumePullRequestBatch(ctx context.Context, options BatchOptions) (BatchResult, error) {
	result := BatchResult{SchemaVersion: 1, Verb: "pr batch resume", Worktree: options.Worktree}
	git := options.git()
	branch, issue := options.checkBatchWorktree(ctx, git)
	if issue != nil {
		return refuseIssue(result, *issue), nil
	}
	state, found, err := options.resolveCache().Load(options.Repository, branch)
	if err != nil {
		return result, err
	}
	if !found {
		return result.refuse(BatchRefusalNotFound, "no batch state for "+branch+" on this machine", "wb pr batch create"), nil
	}
	index := state.conflicted()
	if index < 0 {
		return options.continueBatch(ctx, git, state, result)
	}
	if inMerge, err := git.test(ctx, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err != nil {
		return result, err
	} else if inMerge {
		return result.refuse(BatchRefusalResolutionInvalid, "the merge is still in progress; commit the resolution first", ""), nil
	}
	previous := state.Base
	if index > 0 {
		previous = state.Steps[index-1].MergeCommit
	}
	line, err := git.out(ctx, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return result, err
	}
	commits := strings.Fields(line)
	step := &state.Steps[index]
	if len(commits) != 3 || commits[1] != previous || commits[2] != step.Head {
		return result.refuse(BatchRefusalResolutionInvalid,
			fmt.Sprintf("HEAD %s is not a merge of exactly %s and %s", shortMergeRevision(commits[0]), shortMergeRevision(previous), shortMergeRevision(step.Head)), ""), nil
	}
	step.State, step.MergeCommit = BatchStateResolved, commits[0]
	state.addBuilder(options.resolveOperator().String())
	if err := options.save(&state); err != nil {
		return result, err
	}
	result.Changed = true
	return options.continueBatch(ctx, git, state, result)
}

// loadPublishedState returns the batch state, preferring origin's PR body
// (authoritative) over the private cache, and the batch pull request.
func (options BatchOptions) loadPublishedState(ctx context.Context, branch string) (BatchState, int, bool, error) {
	view, err := options.resolveGitHub().BatchPullRequest(ctx, options.Repository, branch)
	if err != nil {
		return BatchState{}, 0, false, err
	}
	if view.Number != 0 {
		state, ok, parseErr := ParseBatchBody(view.Body)
		if parseErr != nil {
			return BatchState{}, 0, false, parseErr
		}
		if ok {
			// This machine's cache wins only while it extends what origin
			// records: a stop on a conflict is not published until it resolves.
			if cached, found, cacheErr := options.resolveCache().Load(options.Repository, branch); cacheErr != nil {
				return BatchState{}, 0, false, cacheErr
			} else if found && extendsSteps(cached.Steps, state.Steps) {
				state = cached
			}
			state.PullRequest = view.Number
			closed := view.Merged || !strings.EqualFold(view.State, "open")
			return state, view.Number, closed, nil
		}
	}
	state, found, err := options.resolveCache().Load(options.Repository, branch)
	if err != nil || !found {
		return BatchState{}, 0, false, err
	}
	return state, view.Number, false, nil
}

// AddToPullRequestBatch is `wb pr batch add`: refresh the target, then merge
// new pins onto the existing batch head.
func AddToPullRequestBatch(ctx context.Context, options BatchOptions) (BatchResult, error) {
	result := BatchResult{SchemaVersion: 1, Verb: "pr batch add", Worktree: options.Worktree}
	pins, issue := options.parsePins()
	if issue != nil {
		return refuseIssue(result, *issue), nil
	}
	if issue, err := options.checkMergeRoute(ctx); err != nil || issue != nil {
		if err != nil {
			return result, err
		}
		return refuseIssue(result, *issue), nil
	}
	git := options.git()
	branch, issue := options.checkBatchWorktree(ctx, git)
	if issue != nil {
		return refuseIssue(result, *issue), nil
	}
	state, number, closed, err := options.loadPublishedState(ctx, branch)
	if err != nil {
		return result, err
	}
	if state.Version == 0 || number == 0 {
		return result.refuse(BatchRefusalNotFound, "no published batch for "+branch+"; create it, or resume a stopped one", options.verbCommand("resume")), nil
	}
	if closed {
		return result.refuse(BatchRefusalClosed, fmt.Sprintf("batch pull request #%d is merged or closed; start a new batch", number), ""), nil
	}
	// The body block is editable by anyone with write access: the batch's
	// identity comes from the command line and the worktree, and only steps
	// this run verifies may be merged.
	if !strings.EqualFold(state.Repository, options.Repository) || state.Target != options.Target || state.Branch != branch {
		return result.refuse(BatchRefusalBlockInvalid, fmt.Sprintf("the batch block names %s %s into %s, not %s %s into %s", state.Repository, state.Branch, state.Target, options.Repository, branch, options.Target), ""), nil
	}
	if state.conflicted() >= 0 {
		return result.refuse(BatchRefusalConflict, "the batch is stopped on a conflict", options.verbCommand("resume")), nil
	}
	for i, step := range state.Steps {
		if step.State != BatchStateMerged && step.State != BatchStateResolved {
			return result.refuse(BatchRefusalBlockInvalid, fmt.Sprintf("batch step %d is %q, not merged; WB merges only pins it verifies in the same run", i+1, step.State), ""), nil
		}
	}
	if _, err := git.out(ctx, "fetch", "--quiet", "origin", options.Target, state.Branch); err != nil {
		return result, err
	}
	head, err := options.syncBatchHead(ctx, git, state)
	if err != nil || head == "" {
		if err != nil {
			return result, err
		}
		return result.refuse(BatchRefusalMoved, "origin/"+state.Branch+" and this worktree diverged; nothing was force-pushed", ""), nil
	}
	steps, issues, err := options.verifyPins(ctx, git, pins, head, "origin/"+options.Target)
	if err != nil {
		return result, err
	}
	if len(issues) > 0 {
		result.Refusals = issues
		return result.refuse(issues[0].Code, fmt.Sprintf("%d pin(s) refused; the batch is unchanged", len(issues)), ""), nil
	}
	tip, err := git.out(ctx, "rev-parse", "origin/"+options.Target)
	if err != nil {
		return result, err
	}
	if current, err := git.test(ctx, "merge-base", "--is-ancestor", tip, head); err != nil {
		return result, err
	} else if !current {
		state.Steps = append(state.Steps, BatchStep{Kind: BatchStepRefresh, Head: tip, State: BatchStatePending})
	}
	state.Steps = append(state.Steps, steps...)
	state.addBuilder(options.resolveOperator().String())
	return options.continueBatch(ctx, git, state, result)
}

// syncBatchHead brings the worktree to origin's batch head when it is behind,
// and returns the head, or "" when the two diverged.
func (options BatchOptions) syncBatchHead(ctx context.Context, git batchGit, state BatchState) (string, error) {
	head, err := git.out(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	remote, err := git.out(ctx, "rev-parse", "origin/"+state.Branch)
	if err != nil || remote == head {
		return head, err
	}
	if behind, err := git.test(ctx, "merge-base", "--is-ancestor", head, remote); err != nil {
		return "", err
	} else if behind {
		if _, err := git.out(ctx, "merge", "--ff-only", "--quiet", remote); err != nil {
			return "", err
		}
		return remote, nil
	}
	if ahead, err := git.test(ctx, "merge-base", "--is-ancestor", remote, head); err != nil || !ahead {
		return "", err
	}
	return head, nil
}

// extendsSteps reports whether longer starts with every step of shorter.
func extendsSteps(longer, shorter []BatchStep) bool {
	if len(longer) < len(shorter) {
		return false
	}
	for i, step := range shorter {
		if longer[i].Kind != step.Kind || longer[i].Head != step.Head || (step.MergeCommit != "" && longer[i].MergeCommit != step.MergeCommit) {
			return false
		}
	}
	return true
}
