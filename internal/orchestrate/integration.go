package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Integration branches (spec/features/integration-branch-batches): a lane
// pushes its branch with no pull request, a reviewer comments
// `Reviewed-Head: <sha>` on the lane commit, and the operator merges that
// exact commit into the integration branch locally with one merge commit,
// runs the repository's local checks, and pushes. The merge commits' own
// trailers are the only record: there is no batch pull request or cache.

const (
	// BatchRefusalPinInvalid: a pin is not <branch>@<sha>=<commit-comment-url>.
	BatchRefusalPinInvalid = "batch-pin-invalid"
	// BatchRefusalSourceNotOnBranch: the pinned SHA is not a unique commit
	// reachable from origin/<branch>.
	BatchRefusalSourceNotOnBranch = "batch-source-not-on-branch"
	// BatchRefusalSourceUnreviewed: the named commit comment does not bind
	// the pinned SHA.
	BatchRefusalSourceUnreviewed = "batch-source-unreviewed"
	// BatchRefusalSourceSelfReview: the review identity is incomplete or is
	// the lane author's or the operator's own session.
	BatchRefusalSourceSelfReview = "batch-source-self-review"
	// BatchRefusalWorktree: the integration worktree is missing, foreign,
	// dirty, or does not contain origin/<integration>.
	BatchRefusalWorktree = "batch-worktree-invalid"
	// BatchRefusalConflict: a merge stopped on conflicts; resolve, run
	// `git commit --no-edit`, and rerun.
	BatchRefusalConflict = "batch-conflict"
	// IntegrationRefusalCheckFailed: a local check failed; nothing was pushed.
	IntegrationRefusalCheckFailed = "integration-check-failed"
	// IntegrationRefusalMoved: origin's integration branch moved; nothing is
	// force-pushed.
	IntegrationRefusalMoved = "integration-moved"
	// IntegrationRefusalCheckSkipped: the recipe ran no check at all, so
	// nothing was verified; nothing is pushed.
	IntegrationRefusalCheckSkipped = "integration-check-skipped"
	// BatchRefusalUnrecordedMerge: a merge commit whose trailers do not bind
	// its second parent to a still-valid review, and which brings in no
	// target history.
	BatchRefusalUnrecordedMerge = "batch-unrecorded-merge"
)

// Lane merge states.
const (
	LaneStatePending       = "pending"
	LaneStateMerged        = "merged"
	LaneStateAlreadyMerged = "already-merged"
	LaneStateConflicted    = "conflicted"
)

// Trailers every lane merge commit carries.
const (
	trailerLane     = "Wb-Lane"
	trailerReview   = "Wb-Review"
	trailerDigest   = "Wb-Review-Digest"
	trailerReviewer = "Wb-Reviewer"
	trailerMergedBy = "Wb-Merged-By"
	trailerRefresh  = "Wb-Refresh"
)

// batchMergeOptions are the fixed merge settings every lane merge and every
// delta merge-tree run with, so a machine's own Git config never changes
// either result.
var batchMergeOptions = []string{"merge.renames=true", "merge.conflictStyle=merge", "rerere.enabled=false"}

// LanePin is one lane commit as the operator names it.
type LanePin struct {
	Branch          string
	SHA             string
	ReviewURL       string
	ReviewCommentID int64
}

var (
	lanePinPattern          = regexp.MustCompile(`^(\S+)@([0-9a-fA-F]{7,40})=(\S+)$`)
	commitCommentURLPattern = regexp.MustCompile(`^https://github\.com/([^/\s]+/[^/\s]+)/commit/([0-9a-fA-F]{7,40})#commitcomment-([0-9]+)$`)
	// laneBranchPattern keeps a branch name from ever reaching git as an
	// option or a revision expression.
	laneBranchPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/+-]*$`)
)

// validBranchName reports a branch name WB passes to git.
func validBranchName(name string) bool {
	return laneBranchPattern.MatchString(name) && !strings.Contains(name, "..") && !strings.HasSuffix(name, "/") && !strings.HasSuffix(name, ".lock")
}

// ParseLanePin parses `<branch>@<sha>=<commit-comment-url>`.
func ParseLanePin(repository, raw string) (LanePin, error) {
	match := lanePinPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return LanePin{}, fmt.Errorf("pin %q is not <branch>@<sha>=<commit-comment-url>", raw)
	}
	if !validBranchName(match[1]) {
		return LanePin{}, fmt.Errorf("pin %q: %q is not a branch name WB accepts", raw, match[1])
	}
	sha := strings.ToLower(match[2])
	comment := commitCommentURLPattern.FindStringSubmatch(match[3])
	if comment == nil {
		return LanePin{}, fmt.Errorf("pin %q: %q is not a GitHub commit-comment URL", raw, match[3])
	}
	commented := strings.ToLower(comment[2])
	if !strings.EqualFold(comment[1], repository) || (!strings.HasPrefix(commented, sha) && !strings.HasPrefix(sha, commented)) {
		return LanePin{}, fmt.Errorf("pin %q: the review comment is on %s@%s, not %s@%s", raw, comment[1], comment[2], repository, sha)
	}
	id, err := strconv.ParseInt(comment[3], 10, 64)
	if err != nil {
		return LanePin{}, fmt.Errorf("pin %q: comment id %q: %w", raw, comment[3], err)
	}
	return LanePin{Branch: match[1], SHA: sha, ReviewURL: match[3], ReviewCommentID: id}, nil
}

// LaneMerge is one pinned lane and what happened to it.
type LaneMerge struct {
	Branch        string   `json:"branch"`
	SHA           string   `json:"sha"`
	Review        string   `json:"review"`
	ReviewDigest  string   `json:"review_digest,omitempty"`
	Reviewer      string   `json:"reviewer,omitempty"`
	State         string   `json:"state"`
	MergeCommit   string   `json:"merge_commit,omitempty"`
	ConflictPaths []string `json:"conflict_paths,omitempty"`
}

// BatchIssue is one refusal among several reported together.
type BatchIssue struct {
	Code   string `json:"code"`
	Source string `json:"source,omitempty"`
	Reason string `json:"reason"`
}

// IntegrationResult is the envelope `wb pr batch add` and `wb integration
// merge` return.
type IntegrationResult struct {
	SchemaVersion int                         `json:"v"`
	Verb          string                      `json:"verb"`
	Outcome       LandOutcome                 `json:"outcome"`
	RefusalCode   string                      `json:"refusal_code,omitempty"`
	Reason        string                      `json:"reason,omitempty"`
	Refusals      []BatchIssue                `json:"refusals,omitempty"`
	Command       string                      `json:"sanctioned_command,omitempty"`
	Worktree      string                      `json:"worktree,omitempty"`
	Integration   string                      `json:"integration"`
	Head          string                      `json:"head,omitempty"`
	Lanes         []LaneMerge                 `json:"lanes,omitempty"`
	Checks        *quality.VerificationReport `json:"checks,omitempty"`
	Refreshed     string                      `json:"refreshed,omitempty"`
	Pushed        bool                        `json:"pushed"`
	Changed       bool                        `json:"changed"`
}

// ExitCode maps the outcome onto WB's exit contract.
func (result IntegrationResult) ExitCode() int {
	switch result.Outcome {
	case LandSuccess:
		return 0
	case LandRefused:
		return 2
	default:
		return 1
	}
}

func (result IntegrationResult) refuse(code, reason, command string) IntegrationResult {
	result.Outcome, result.RefusalCode, result.Reason, result.Command = LandRefused, code, reason, command
	return result
}

func (result IntegrationResult) finding(code, reason, command string) IntegrationResult {
	result.Outcome, result.RefusalCode, result.Reason, result.Command = LandFindings, code, reason, command
	return result
}

// MarshalIntegrationResult renders the JSON envelope.
func MarshalIntegrationResult(result IntegrationResult) ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}

// IntegrationText renders the text form of a result.
func IntegrationText(result IntegrationResult) string {
	var text strings.Builder
	fmt.Fprintf(&text, "%s: %s\n", result.Outcome, result.Reason)
	if result.RefusalCode != "" {
		fmt.Fprintf(&text, "refusal: %s\n", result.RefusalCode)
	}
	for _, issue := range result.Refusals {
		fmt.Fprintf(&text, "  %s %s: %s\n", issue.Code, issue.Source, issue.Reason)
	}
	for _, lane := range result.Lanes {
		fmt.Fprintf(&text, "  %s@%s %s", lane.Branch, shortMergeRevision(lane.SHA), lane.State)
		if len(lane.ConflictPaths) > 0 {
			fmt.Fprintf(&text, " conflicts: %s", strings.Join(lane.ConflictPaths, ", "))
		}
		text.WriteString("\n")
	}
	if result.Checks != nil {
		for _, entry := range result.Checks.Results {
			if entry.Status == quality.StatusFailed {
				fmt.Fprintf(&text, "  check %s failed: %s\n", entry.Check, strings.TrimSpace(entry.Detail))
			}
		}
	}
	if result.Command != "" {
		fmt.Fprintf(&text, "next: %s\n", result.Command)
	}
	return text.String()
}

// IntegrationOptions configures `wb pr batch add` and `wb integration merge`.
type IntegrationOptions struct {
	Repository   string
	Integration  string
	Worktree     string
	Pins         []string
	ProjectsRoot string
	// Checks are the local checks `wb integration merge` runs before it
	// pushes; empty means the repository's integration_checks recipe, else
	// defaultIntegrationChecks.
	Checks []quality.Check
	// CheckTimeout bounds the local checks; 0 is the quality default.
	CheckTimeout time.Duration
	// To is the promotion target; empty means the repository's default
	// branch. `wb integration merge` refreshes the integration branch from it
	// whenever the branch does not contain it.
	To     string
	GitHub IntegrationGitHub

	verb        string
	run         runner.Runner
	checkout    func(worktree string) (repository string, err error)
	laneSession func(projectsRoot, repository, branch string) string
	operator    func() ReviewerIdentity
	verify      func(ctx context.Context, repository, dir string, checks []quality.Check, timeout time.Duration) quality.VerificationReport
}

func (options IntegrationOptions) checkoutGit() batchGit {
	run := options.run
	if run == nil {
		run = defaultRunner
	}
	return batchGit{run: run, dir: options.Worktree}
}

func (options IntegrationOptions) resolveGitHub() IntegrationGitHub {
	if options.GitHub != nil {
		return options.GitHub
	}
	return DefaultIntegrationGitHub()
}

func (options IntegrationOptions) resolveCheckout() func(string) (string, error) {
	if options.checkout != nil {
		return options.checkout
	}
	return func(worktree string) (string, error) {
		manifest, err := worktrees.ReadManifest(worktree)
		return manifest.Repository, err
	}
}

func (options IntegrationOptions) resolveLaneSession() func(string, string, string) string {
	if options.laneSession != nil {
		return options.laneSession
	}
	return claimSessionForBranch
}

func (options IntegrationOptions) resolveOperator() ReviewerIdentity {
	if options.operator != nil {
		return options.operator()
	}
	return currentSessionIdentity()
}

func (options IntegrationOptions) resolveVerify() func(context.Context, string, string, []quality.Check, time.Duration) quality.VerificationReport {
	if options.verify != nil {
		return options.verify
	}
	return localChecks
}

// defaultIntegrationChecks run when neither --checks nor the repository's
// integration_checks recipe names any; typecheck runs only where a project
// defines it.
var defaultIntegrationChecks = []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild, quality.CheckTypecheck}

// localChecks runs the repository's own local-check recipe, as `wb worktree
// merge` validates a candidate: explicit checks, else the repository's
// integration_checks, else defaultIntegrationChecks.
func localChecks(ctx context.Context, repository, dir string, checks []quality.Check, timeout time.Duration) quality.VerificationReport {
	runOptions, err := quality.RepositoryRunOptions(dir, quality.RunOptions{Timeout: timeout})
	if err != nil {
		return quality.VerificationReport{Repository: repository, Path: dir, Status: quality.StatusFailed,
			Results: []quality.VerificationEntry{{Status: quality.StatusFailed, Detail: err.Error()}}}
	}
	switch {
	case len(checks) > 0:
	case len(runOptions.IntegrationChecks) > 0:
		checks = runOptions.IntegrationChecks
	default:
		checks = defaultIntegrationChecks
	}
	return quality.VerifyWithOptions(ctx, repository, dir, checks, runOptions)
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

func (options IntegrationOptions) command(verb string) string {
	return fmt.Sprintf("wb %s %s %s --worktree %s", verb, options.Repository, options.integrationArg(verb), shellSingleQuote(options.Worktree))
}

// integrationArg spells the integration branch the way verb takes it.
func (options IntegrationOptions) integrationArg(verb string) string {
	if strings.HasPrefix(verb, "pr batch") {
		return "--target " + options.Integration
	}
	return options.Integration
}

// rerunCommand is the same invocation again, pins quoted.
func (options IntegrationOptions) rerunCommand() string {
	command := options.command(options.verb)
	if options.To != "" {
		command += " --to " + shellSingleQuote(options.To)
	}
	for _, pin := range options.Pins {
		command += " " + shellSingleQuote(pin)
	}
	return command
}

func (options IntegrationOptions) remoteRef() string {
	return "refs/remotes/origin/" + options.Integration
}

// checkWorktree applies REQ batch-construction's worktree rule and fetches
// origin/<integration>.
func (options IntegrationOptions) checkWorktree(ctx context.Context, git batchGit) *BatchIssue {
	create := fmt.Sprintf("wb worktree create <task> %s --base %s --model <model> --original-prompt-file -", options.Repository, options.Integration)
	refuse := func(reason string) *BatchIssue {
		return &BatchIssue{Code: BatchRefusalWorktree, Reason: reason + "; create one with: " + create}
	}
	if strings.TrimSpace(options.Worktree) == "" {
		return refuse("no integration worktree given")
	}
	repository, err := options.resolveCheckout()(options.Worktree)
	switch {
	case err != nil:
		return refuse(fmt.Sprintf("%s is not a WB-managed worktree: %v", options.Worktree, err))
	case !strings.EqualFold(repository, options.Repository):
		return refuse(fmt.Sprintf("%s is a worktree of %s, not %s", options.Worktree, repository, options.Repository))
	}
	status, err := git.out(ctx, "status", "--porcelain")
	if err != nil {
		return refuse(err.Error())
	}
	if status != "" {
		return refuse(fmt.Sprintf("%s has uncommitted changes or a merge in progress (commit a resolution with git commit --no-edit)", options.Worktree))
	}
	if _, err := git.out(ctx, "fetch", "--quiet", "origin", "+refs/heads/"+options.Integration+":"+options.remoteRef()); err != nil {
		return refuse(fmt.Sprintf("fetch origin %s: %v", options.Integration, err))
	}
	contains, err := git.test(ctx, "merge-base", "--is-ancestor", options.remoteRef(), "HEAD")
	if err != nil {
		return refuse(err.Error())
	}
	if !contains {
		return refuse(fmt.Sprintf("HEAD of %s does not contain origin/%s", options.Worktree, options.Integration))
	}
	return nil
}

// verifyPins applies REQ batch-source-pins to every pin and returns the
// lanes, or every refusal together.
func (options IntegrationOptions) verifyPins(ctx context.Context, git batchGit, pins []LanePin) ([]LaneMerge, []BatchIssue, error) {
	github := options.resolveGitHub()
	operator := options.resolveOperator()
	var lanes []LaneMerge
	var issues []BatchIssue
	for _, pin := range pins {
		label := pin.Branch + "@" + pin.SHA
		issue := func(code, reason string) {
			issues = append(issues, BatchIssue{Code: code, Source: label, Reason: reason})
		}
		remote := "refs/remotes/origin/" + pin.Branch
		if result, err := git.exec(ctx, "fetch", "--quiet", "origin", "+refs/heads/"+pin.Branch+":"+remote); err != nil {
			if !strings.Contains(result.Stderr, "couldn't find remote ref") {
				return nil, nil, fmt.Errorf("fetch origin %s: %w: %s", pin.Branch, err, strings.TrimSpace(result.Stderr))
			}
			issue(BatchRefusalSourceNotOnBranch, fmt.Sprintf("origin has no branch %s", pin.Branch))
			continue
		}
		full, err := git.out(ctx, "rev-parse", "--verify", "--quiet", pin.SHA+"^{commit}")
		if err != nil {
			issue(BatchRefusalSourceNotOnBranch, fmt.Sprintf("%s is not a unique commit", pin.SHA))
			continue
		}
		if onBranch, err := git.test(ctx, "merge-base", "--is-ancestor", full, remote); err != nil {
			return nil, nil, err
		} else if !onBranch {
			issue(BatchRefusalSourceNotOnBranch, fmt.Sprintf("%s is not on origin/%s", shortMergeRevision(full), pin.Branch))
			continue
		}
		lane := LaneMerge{Branch: pin.Branch, SHA: full, Review: pin.ReviewURL, State: LaneStatePending}
		if merged, err := git.test(ctx, "merge-base", "--is-ancestor", full, "HEAD"); err != nil {
			return nil, nil, err
		} else if merged {
			// Only through a recorded merge: a resolution committed without
			// --no-edit loses the trailers, and a rerun must not skip past it.
			recorded, err := options.mergedThroughRecord(ctx, git, full)
			if err != nil {
				return nil, nil, err
			}
			if !recorded {
				issue(BatchRefusalUnrecordedMerge, fmt.Sprintf("%s is in HEAD only through a merge without its Wb-Lane trailers; amend that merge with git commit --amend and the trailers WB printed, or recreate the worktree", shortMergeRevision(full)))
				continue
			}
			lane.State = LaneStateAlreadyMerged
			lanes = append(lanes, lane)
			continue
		}
		comment, err := github.CommitComment(ctx, options.Repository, pin.ReviewCommentID)
		if err != nil {
			issue(BatchRefusalSourceUnreviewed, err.Error())
			continue
		}
		if !strings.EqualFold(comment.CommitID, full) {
			issue(BatchRefusalSourceUnreviewed, fmt.Sprintf("review comment %d is on %s, not %s", pin.ReviewCommentID, shortMergeRevision(comment.CommitID), shortMergeRevision(full)))
			continue
		}
		if reviewed, parseErr := parseReviewedHeadLine(comment.Body); parseErr != nil || reviewed != full {
			issue(BatchRefusalSourceUnreviewed, fmt.Sprintf("review %s does not end Reviewed-Head: %s", pin.ReviewURL, full))
			continue
		}
		reviewer := declaredReviewer(comment.Body)
		author := options.resolveLaneSession()(options.ProjectsRoot, options.Repository, pin.Branch)
		// Independence is judged on the session alone: the same session
		// declaring another model name is still the same author.
		if !identityComplete(reviewer) || (author != "" && strings.EqualFold(reviewer.Session, author)) || strings.EqualFold(reviewer.Session, operator.Session) {
			issue(BatchRefusalSourceSelfReview, fmt.Sprintf("review identity %q is incomplete, the lane author's, or the operator's", reviewer.String()))
			continue
		}
		lane.ReviewDigest, lane.Reviewer = ReviewDigest(comment.Body), reviewer.String()
		lanes = append(lanes, lane)
	}
	return lanes, issues, nil
}

// mergedThroughRecord reports whether sha, already in HEAD, is either on
// origin/<integration> or brought in by a local first-parent merge whose
// Wb-Lane trailer names its own second parent, a commit that contains sha.
func (options IntegrationOptions) mergedThroughRecord(ctx context.Context, git batchGit, sha string) (bool, error) {
	if pushed, err := git.test(ctx, "merge-base", "--is-ancestor", sha, options.remoteRef()); err != nil || pushed {
		return pushed, err
	}
	raw, err := git.out(ctx, "log", "--first-parent", "--merges", "--format=%P%x00%B%x1e", "^"+options.remoteRef(), "HEAD")
	if err != nil {
		return false, err
	}
	for _, entry := range strings.Split(raw, "\x1e") {
		parents, message, _ := strings.Cut(strings.TrimLeft(entry, "\n"), "\x00")
		fields := strings.Fields(parents)
		record, ok := parseLaneTrailers(message)
		if !ok || len(fields) != 2 || fields[1] != record.SHA {
			continue
		}
		if contains, err := git.test(ctx, "merge-base", "--is-ancestor", sha, record.SHA); err != nil || contains {
			return contains, err
		}
	}
	return false, nil
}

// refreshFromTarget resolves and fetches the promotion target and, when the
// integration branch does not contain it (the target moved, or a promotion
// squashed), merges the target into the integration branch as a recorded
// refresh, so the promotion pull request is never behind its target. It returns the target's remote ref, the
// target SHA it merged ("" when none was needed), and a finding when the
// refresh conflicts or the target is not a branch WB accepts.
func (options IntegrationOptions) refreshFromTarget(ctx context.Context, git batchGit) (string, string, func(IntegrationResult) IntegrationResult, error) {
	github := options.resolveGitHub()
	to := options.To
	if to == "" {
		var err error
		if to, err = github.DefaultBranch(ctx, options.Repository); err != nil {
			return "", "", nil, err
		}
	}
	if !validBranchName(to) {
		return "", "", func(result IntegrationResult) IntegrationResult {
			return result.refuse(BatchRefusalPinInvalid, fmt.Sprintf("%q is not a branch name WB accepts", to), "")
		}, nil
	}
	ref := "refs/remotes/origin/" + to
	if _, err := git.out(ctx, "fetch", "--quiet", "origin", "+refs/heads/"+to+":"+ref); err != nil {
		return "", "", nil, err
	}
	if contained, err := git.test(ctx, "merge-base", "--is-ancestor", ref, "HEAD"); err != nil || contained {
		return ref, "", nil, err
	}
	tip, err := git.out(ctx, "rev-parse", ref)
	if err != nil {
		return ref, "", nil, err
	}
	subject := fmt.Sprintf("Merge %s (%s) into %s", to, shortMergeRevision(tip), options.Integration)
	trailers := fmt.Sprintf("%s: %s@%s\n%s: %s", trailerRefresh, to, tip, trailerMergedBy, options.resolveOperator().String())
	if _, mergeErr := git.out(ctx, configArgs(batchMergeOptions, "merge", "--no-ff", "--no-edit", "-m", subject, "-m", trailers, tip)...); mergeErr != nil {
		conflicts, listErr := git.out(ctx, "diff", "--name-only", "--diff-filter=U")
		if listErr != nil || conflicts == "" {
			return ref, "", nil, mergeErr
		}
		return ref, tip, func(result IntegrationResult) IntegrationResult {
			return result.finding(BatchRefusalConflict,
				fmt.Sprintf("refreshing from %s conflicts in %s; resolve them in %s, run git commit --no-edit, and rerun",
					to, strings.ReplaceAll(conflicts, "\n", ", "), options.Worktree), options.rerunCommand())
		}, nil
	}
	return ref, tip, nil, nil
}

// unrecordedLocalMerge returns the first local first-parent merge, not yet on
// origin/<integration>, that neither names its own second parent in Wb-Lane
// trailers nor brings in target history.
func (options IntegrationOptions) unrecordedLocalMerge(ctx context.Context, git batchGit, target string) (string, error) {
	raw, err := git.out(ctx, "log", "--first-parent", "--merges", "--format=%H%x00%P%x00%B%x1e", "^"+options.remoteRef(), "HEAD")
	if err != nil {
		return "", err
	}
	for _, entry := range strings.Split(raw, "\x1e") {
		fields := strings.SplitN(strings.TrimLeft(entry, "\n"), "\x00", 3)
		if len(fields) != 3 {
			continue
		}
		parents := strings.Fields(fields[1])
		if record, ok := parseLaneTrailers(fields[2]); ok && len(parents) == 2 && parents[1] == record.SHA {
			continue
		}
		if len(parents) == 2 {
			refresh, err := git.test(ctx, "merge-base", "--is-ancestor", parents[1], target)
			if err != nil {
				return "", err
			}
			if refresh {
				continue
			}
		}
		return fields[0], nil
	}
	return "", nil
}

// laneRecord is what a lane merge commit's trailers claim.
type laneRecord struct {
	Branch    string
	SHA       string
	Review    string
	CommentID int64
	Digest    string
	Reviewer  string
	MergedBy  ReviewerIdentity
}

var (
	trailerLinePattern = regexp.MustCompile(`(?m)^(Wb-[A-Za-z-]+): (.+)$`)
	fullSHAPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// parseLaneTrailers reads a lane merge commit's trailers; ok is false unless
// every one is present and well formed.
func parseLaneTrailers(message string) (laneRecord, bool) {
	values := map[string]string{}
	for _, match := range trailerLinePattern.FindAllStringSubmatch(message, -1) {
		values[match[1]] = strings.TrimSpace(match[2])
	}
	lane := values[trailerLane]
	at := strings.LastIndex(lane, "@")
	if at < 0 {
		return laneRecord{}, false
	}
	record := laneRecord{Branch: lane[:at], SHA: strings.ToLower(lane[at+1:]), Review: values[trailerReview],
		Digest: values[trailerDigest], Reviewer: values[trailerReviewer], MergedBy: ParseReviewerIdentity(values[trailerMergedBy])}
	comment := commitCommentURLPattern.FindStringSubmatch(record.Review)
	if !fullSHAPattern.MatchString(record.SHA) || comment == nil || record.Digest == "" || !identityComplete(record.MergedBy) {
		return laneRecord{}, false
	}
	record.CommentID, _ = strconv.ParseInt(comment[3], 10, 64) // the pattern admits digits only
	return record, true
}

// laneTrailers is the trailer block a lane merge commit carries.
func laneTrailers(lane LaneMerge, operator ReviewerIdentity) string {
	return fmt.Sprintf("%s: %s@%s\n%s: %s\n%s: %s\n%s: %s\n%s: %s",
		trailerLane, lane.Branch, lane.SHA, trailerReview, lane.Review, trailerDigest, lane.ReviewDigest,
		trailerReviewer, lane.Reviewer, trailerMergedBy, operator.String())
}

// mergeLane makes the lane's merge commit, or returns the conflicting paths
// with the merge left in progress.
func (options IntegrationOptions) mergeLane(ctx context.Context, git batchGit, lane *LaneMerge, operator ReviewerIdentity) ([]string, error) {
	subject := fmt.Sprintf("Merge %s (%s) into %s", lane.Branch, shortMergeRevision(lane.SHA), options.Integration)
	if _, err := git.out(ctx, configArgs(batchMergeOptions, "merge", "--no-ff", "--no-edit", "-m", subject, "-m", laneTrailers(*lane, operator), lane.SHA)...); err != nil {
		conflicts, listErr := git.out(ctx, "diff", "--name-only", "--diff-filter=U")
		if listErr != nil || conflicts == "" {
			return nil, err
		}
		return strings.Split(conflicts, "\n"), nil
	}
	merge, err := git.out(ctx, "rev-parse", "HEAD")
	lane.MergeCommit, lane.State = merge, LaneStateMerged
	return nil, err
}

func (options IntegrationOptions) parsePins() ([]LanePin, *BatchIssue) {
	pins := make([]LanePin, 0, len(options.Pins))
	for _, raw := range options.Pins {
		pin, err := ParseLanePin(options.Repository, raw)
		if err != nil {
			return nil, &BatchIssue{Code: BatchRefusalPinInvalid, Reason: err.Error()}
		}
		pins = append(pins, pin)
	}
	return pins, nil
}

func (options IntegrationOptions) newResult() IntegrationResult {
	return IntegrationResult{SchemaVersion: 1, Verb: options.verb, Worktree: options.Worktree, Integration: options.Integration}
}

// AddToBatch is `wb pr batch add`: verify every pin, then merge each one into
// the integration worktree with its own merge commit. It never pushes.
func AddToBatch(ctx context.Context, options IntegrationOptions) (IntegrationResult, error) {
	if options.verb == "" {
		options.verb = "pr batch add"
	}
	result := options.newResult()
	if !validBranchName(options.Integration) {
		return result.refuse(BatchRefusalPinInvalid, fmt.Sprintf("%q is not a branch name WB accepts", options.Integration), ""), nil
	}
	pins, issue := options.parsePins()
	if issue != nil {
		return result.refuse(issue.Code, issue.Reason, ""), nil
	}
	git := options.checkoutGit()
	if issue := options.checkWorktree(ctx, git); issue != nil {
		return result.refuse(issue.Code, issue.Reason, ""), nil
	}
	lanes, issues, err := options.verifyPins(ctx, git, pins)
	if err != nil {
		return result, err
	}
	if len(issues) > 0 {
		result.Refusals = issues
		return result.refuse(issues[0].Code, fmt.Sprintf("%d of %d pins refused; nothing was merged", len(issues), len(pins)), ""), nil
	}
	operator := options.resolveOperator()
	merged := 0
	for i := range lanes {
		lane := &lanes[i]
		if lane.State == LaneStateAlreadyMerged {
			continue
		}
		// An earlier pin of this run may already contain this one.
		if contained, err := git.test(ctx, "merge-base", "--is-ancestor", lane.SHA, "HEAD"); err != nil {
			return result, err
		} else if contained {
			lane.State = LaneStateAlreadyMerged
			continue
		}
		conflicts, err := options.mergeLane(ctx, git, lane, operator)
		if err != nil {
			return result, err
		}
		if conflicts != nil {
			lane.State, lane.ConflictPaths = LaneStateConflicted, conflicts
			result.Lanes, result.Changed = lanes, true
			return result.finding(BatchRefusalConflict,
				fmt.Sprintf("merging %s@%s conflicts in %s; resolve them in %s, run git commit --no-edit, and rerun", lane.Branch, shortMergeRevision(lane.SHA), strings.Join(conflicts, ", "), options.Worktree),
				options.rerunCommand()), nil
		}
		merged++
	}
	result.Lanes, result.Changed = lanes, merged > 0
	if result.Head, err = git.out(ctx, "rev-parse", "HEAD"); err != nil {
		return result, err
	}
	result.Outcome = LandSuccess
	result.Reason = fmt.Sprintf("merged %d lane(s), %d already merged", merged, len(lanes)-merged)
	if options.verb == "pr batch add" {
		result.Command = options.command("integration merge")
	}
	return result, nil
}

// MergeIntegration is `wb integration merge`: AddToBatch for any pins, then
// the repository's local checks on the result, then a push without force.
func MergeIntegration(ctx context.Context, options IntegrationOptions) (IntegrationResult, error) {
	options.verb = "integration merge"
	result := options.newResult()
	git := options.checkoutGit()
	if len(options.Pins) > 0 {
		added, err := AddToBatch(ctx, options)
		if err != nil || added.Outcome != LandSuccess {
			return added, err
		}
		result = added
	} else {
		if !validBranchName(options.Integration) {
			return result.refuse(BatchRefusalPinInvalid, fmt.Sprintf("%q is not a branch name WB accepts", options.Integration), ""), nil
		}
		if issue := options.checkWorktree(ctx, git); issue != nil {
			return result.refuse(issue.Code, issue.Reason, ""), nil
		}
	}
	target, refreshed, finding, err := options.refreshFromTarget(ctx, git)
	if err != nil {
		return result, err
	}
	result.Refreshed = refreshed
	if finding != nil {
		return finding(result), nil
	}
	head, err := git.out(ctx, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	remote, err := git.out(ctx, "rev-parse", options.remoteRef())
	if err != nil {
		return result, err
	}
	result.Head, result.Command = head, ""
	if head == remote {
		result.Outcome, result.Reason = LandSuccess, fmt.Sprintf("origin/%s is already at %s; nothing to check or push", options.Integration, shortMergeRevision(head))
		return result, nil
	}
	// Whatever is pushed must be what WB merged: every local merge carries
	// trailers naming its own second parent.
	if unrecorded, err := options.unrecordedLocalMerge(ctx, git, target); err != nil {
		return result, err
	} else if unrecorded != "" {
		return result.refuse(BatchRefusalUnrecordedMerge,
			fmt.Sprintf("local merge %s carries no Wb-Lane trailers naming its second parent; nothing was checked or pushed", shortMergeRevision(unrecorded)), ""), nil
	}
	report := options.resolveVerify()(ctx, options.Repository, options.Worktree, options.Checks, options.CheckTimeout)
	result.Checks = &report
	switch report.Status {
	case quality.StatusPassed:
	case quality.StatusSkipped:
		return result.refuse(IntegrationRefusalCheckSkipped,
			fmt.Sprintf("the local-check recipe ran no check on %s, so nothing was verified; nothing was pushed", shortMergeRevision(head)), ""), nil
	default:
		return result.finding(IntegrationRefusalCheckFailed,
			fmt.Sprintf("the local checks failed on %s; nothing was pushed", shortMergeRevision(head)), options.command("integration merge")), nil
	}
	// Push exactly the commit that was checked.
	if pushed, err := git.exec(ctx, "push", "--quiet", "origin", head+":refs/heads/"+options.Integration); err != nil {
		stderr := pushed.Stderr + pushed.Stdout
		if strings.Contains(stderr, "[rejected]") || strings.Contains(stderr, "non-fast-forward") || strings.Contains(stderr, "fetch first") {
			return result.refuse(IntegrationRefusalMoved,
				fmt.Sprintf("origin/%s moved; nothing was force-pushed. Rerun in a fresh worktree of %s: merged pins are skipped", options.Integration, options.Integration), ""), nil
		}
		return result, fmt.Errorf("push %s: %w: %s", options.Integration, err, strings.TrimSpace(stderr))
	}
	result.Outcome, result.Pushed, result.Changed = LandSuccess, true, true
	result.Reason = fmt.Sprintf("checked %s locally and pushed it to %s", shortMergeRevision(head), options.Integration)
	return result, nil
}
