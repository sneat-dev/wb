package orchestrate

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/runner"
)

// Promotion of an integration branch (spec/features/integration-branch-batches,
// REQs batch-merge-provenance, batch-delta and integration-promote): every
// commit on the integration branch that its promotion target lacks is
// re-proved from the merge commits' own trailers, the reviewer reads only the
// integration delta, and one pull request carries the branch to its target.

// promoteMarker identifies a promotion pull request body.
const promoteMarker = "<!-- wb-integration-promote -->"

// Delta part kinds.
const (
	BatchDeltaResolution = "resolution"
	BatchDeltaFix        = "fix"
	BatchDeltaUnrecorded = "unrecorded"
	BatchDeltaOverlap    = "overlap"
)

// BatchDelta is one part of the integration delta.
type BatchDelta struct {
	Kind   string   `json:"kind"`
	Commit string   `json:"commit"`
	Lane   string   `json:"lane,omitempty"`
	Paths  []string `json:"paths"`
	Diff   string   `json:"diff"`
}

// integrationCommit is one commit on the first-parent walk, newest first.
type integrationCommit struct {
	SHA     string
	Parents []string
	Message string
}

// provenance is the verdict on one first-parent walk.
type provenance struct {
	Lanes      []LaneMerge
	Unrecorded []string
	Fixes      []string
	Merges     []integrationCommit
	// MergedBy are the sessions that made the recorded lane merges; none of
	// them may review the promotion.
	MergedBy []string
}

func (verdict provenance) mergedBy(session string) bool {
	for _, merger := range verdict.MergedBy {
		if strings.EqualFold(merger, session) {
			return true
		}
	}
	return false
}

// verifyProvenance applies REQ batch-merge-provenance to walk. inTarget
// reports whether a commit is history of the promotion target.
func verifyProvenance(ctx context.Context, github commitCommentReader, repository string, walk []integrationCommit, inTarget func(string) (bool, error)) (provenance, error) {
	var verdict provenance
	for _, commit := range walk {
		if len(commit.Parents) < 2 {
			verdict.Fixes = append(verdict.Fixes, commit.SHA)
			continue
		}
		verdict.Merges = append(verdict.Merges, commit)
		second := strings.ToLower(commit.Parents[1])
		if len(commit.Parents) == 2 {
			refresh, err := inTarget(second)
			if err != nil {
				return provenance{}, err
			}
			if refresh {
				continue
			}
		}
		record, ok := parseLaneTrailers(commit.Message)
		if !ok || len(commit.Parents) != 2 || record.SHA != second || !reviewStillBinds(ctx, github, repository, record) {
			verdict.Unrecorded = append(verdict.Unrecorded, commit.SHA)
			continue
		}
		verdict.Lanes = append(verdict.Lanes, LaneMerge{Branch: record.Branch, SHA: record.SHA, Review: record.Review,
			ReviewDigest: record.Digest, Reviewer: record.Reviewer, State: LaneStateMerged, MergeCommit: commit.SHA})
		verdict.MergedBy = append(verdict.MergedBy, record.MergedBy.Session)
	}
	return verdict, nil
}

// reviewStillBinds re-reads a lane's review: still on that commit, still the
// recorded text, still naming it, by a complete identity that did not make
// the merge. A comment that cannot be read does not bind.
func reviewStillBinds(ctx context.Context, github commitCommentReader, repository string, record laneRecord) bool {
	comment, err := github.CommitComment(ctx, repository, record.CommentID)
	if err != nil || !strings.EqualFold(comment.CommitID, record.SHA) || ReviewDigest(comment.Body) != record.Digest {
		return false
	}
	reviewed, err := parseReviewedHeadLine(comment.Body)
	reviewer := declaredReviewer(comment.Body)
	return err == nil && reviewed == record.SHA && identityComplete(reviewer) && !strings.EqualFold(reviewer.Session, record.MergedBy.Session)
}

// PromoteOptions configures `wb pr batch delta` and `wb integration promote`.
type PromoteOptions struct {
	Repository  string
	Integration string
	// To is the promotion target; empty means the repository's default
	// branch.
	To       string
	Worktree string
	// Overlap adds each lane's own diff of every file more than one lane
	// changed: the cheap place to look for a semantic conflict that produced
	// no textual one.
	Overlap bool
	GitHub  PromoteGitHub

	verb     string
	run      runner.Runner
	checkout func(worktree string) (repository string, err error)
}

// PromoteGitHub is every GitHub read and write promotion makes.
type PromoteGitHub interface {
	IntegrationGitHub
	// OpenPullRequest finds the open pull request from head to base; number
	// 0 means there is none.
	OpenPullRequest(ctx context.Context, repository, head, base string) (number int, body string, err error)
	CreatePullRequest(ctx context.Context, repository, head, base, title, body string) (int, error)
	UpdatePullRequestBody(ctx context.Context, repository string, number int, body string) error
}

// PromoteResult is the envelope `wb pr batch delta` and `wb integration
// promote` return.
type PromoteResult struct {
	SchemaVersion int          `json:"v"`
	Verb          string       `json:"verb"`
	Outcome       LandOutcome  `json:"outcome"`
	RefusalCode   string       `json:"refusal_code,omitempty"`
	Reason        string       `json:"reason,omitempty"`
	Command       string       `json:"sanctioned_command,omitempty"`
	Integration   string       `json:"integration"`
	To            string       `json:"to"`
	Head          string       `json:"head,omitempty"`
	Lanes         []LaneMerge  `json:"lanes,omitempty"`
	Unrecorded    []string     `json:"unrecorded,omitempty"`
	Delta         []BatchDelta `json:"delta,omitempty"`
	PullRequest   string       `json:"pull_request,omitempty"`
	Changed       bool         `json:"changed"`
}

// ExitCode maps the outcome onto WB's exit contract.
func (result PromoteResult) ExitCode() int {
	return IntegrationResult{Outcome: result.Outcome}.ExitCode()
}

func (result PromoteResult) refuse(code, reason, command string) PromoteResult {
	result.Outcome, result.RefusalCode, result.Reason, result.Command = LandRefused, code, reason, command
	return result
}

// PromoteText renders the text form of a result.
func PromoteText(result PromoteResult) string {
	var text strings.Builder
	fmt.Fprintf(&text, "%s: %s\n", result.Outcome, result.Reason)
	if result.RefusalCode != "" {
		fmt.Fprintf(&text, "refusal: %s\n", result.RefusalCode)
	}
	if result.PullRequest != "" {
		fmt.Fprintf(&text, "pull request: %s\n", result.PullRequest)
	}
	if result.Head != "" {
		fmt.Fprintf(&text, "head: %s (a review of this delta ends Reviewed-Head: %s)\n", result.Head, result.Head)
	}
	for _, lane := range result.Lanes {
		fmt.Fprintf(&text, "  lane %s@%s reviewed by %s\n", lane.Branch, shortMergeRevision(lane.SHA), lane.Reviewer)
	}
	for _, commit := range result.Unrecorded {
		fmt.Fprintf(&text, "  unrecorded merge %s\n", commit)
	}
	for _, part := range result.Delta {
		label := part.Kind + " " + part.Commit
		if part.Lane != "" {
			label += " " + part.Lane
		}
		fmt.Fprintf(&text, "--- %s (%s)\n%s\n", label, strings.Join(part.Paths, ", "), part.Diff)
	}
	if result.Command != "" {
		fmt.Fprintf(&text, "next: %s\n", result.Command)
	}
	return text.String()
}

func (options PromoteOptions) checkoutGit() batchGit {
	return IntegrationOptions{Worktree: options.Worktree, run: options.run}.checkoutGit()
}

func (options PromoteOptions) resolveGitHub() PromoteGitHub {
	if options.GitHub != nil {
		return options.GitHub
	}
	return DefaultPromoteGitHub()
}

// open checks the checkout, resolves the target, fetches both branches and
// reads the first-parent walk of what the target lacks.
func (options *PromoteOptions) open(ctx context.Context, git batchGit, result *PromoteResult) (*PromoteResult, []integrationCommit, error) {
	refuse := func(reason string) (*PromoteResult, []integrationCommit, error) {
		refused := result.refuse(BatchRefusalWorktree, reason, "")
		return &refused, nil, nil
	}
	if !validBranchName(options.Integration) || (options.To != "" && !validBranchName(options.To)) {
		return refuse(fmt.Sprintf("%q or %q is not a branch name WB accepts", options.Integration, options.To))
	}
	if strings.TrimSpace(options.Worktree) == "" {
		return refuse("no checkout given")
	}
	repository, err := IntegrationOptions{checkout: options.checkout}.resolveCheckout()(options.Worktree)
	if err != nil || !strings.EqualFold(repository, options.Repository) {
		return refuse(fmt.Sprintf("%s is not a WB-managed worktree of %s (%v)", options.Worktree, options.Repository, err))
	}
	if options.To == "" {
		if options.To, err = options.resolveGitHub().DefaultBranch(ctx, options.Repository); err != nil {
			return nil, nil, err
		}
	}
	result.To = options.To
	for _, branch := range []string{options.Integration, options.To} {
		if _, err := git.out(ctx, "fetch", "--quiet", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
			return refuse(fmt.Sprintf("fetch origin %s: %v", branch, err))
		}
	}
	if result.Head, err = git.out(ctx, "rev-parse", "refs/remotes/origin/"+options.Integration); err != nil {
		return nil, nil, err
	}
	walk, err := localWalk(ctx, git, "refs/remotes/origin/"+options.To, result.Head)
	return nil, walk, err
}

// localWalk lists the first-parent commits of head the target lacks, newest
// first.
func localWalk(ctx context.Context, git batchGit, target, head string) ([]integrationCommit, error) {
	raw, err := git.out(ctx, "log", "--first-parent", "--format=%H%x00%P%x00%B%x1e", "^"+target, head)
	if err != nil {
		return nil, err
	}
	var walk []integrationCommit
	for _, record := range strings.Split(raw, "\x1e") {
		fields := strings.SplitN(strings.TrimLeft(record, "\n"), "\x00", 3)
		if len(fields) != 3 {
			continue
		}
		walk = append(walk, integrationCommit{SHA: fields[0], Parents: strings.Fields(fields[1]), Message: fields[2]})
	}
	return walk, nil
}

func localInTarget(ctx context.Context, git batchGit, target string) func(string) (bool, error) {
	return func(sha string) (bool, error) {
		return git.test(ctx, "merge-base", "--is-ancestor", sha, target)
	}
}

func (options PromoteOptions) deltaCommand() string {
	return fmt.Sprintf("wb pr batch delta %s --target %s --to %s", options.Repository, shellSingleQuote(options.Integration), shellSingleQuote(options.To))
}

// DeltaBatch is `wb pr batch delta`: only the integration delta of the
// integration branch against its promotion target.
func DeltaBatch(ctx context.Context, options PromoteOptions) (PromoteResult, error) {
	options.verb = "pr batch delta"
	result := PromoteResult{SchemaVersion: 1, Verb: options.verb, Integration: options.Integration, To: options.To}
	git := options.checkoutGit()
	refused, walk, err := options.open(ctx, git, &result)
	if refused != nil || err != nil {
		return derefPromote(refused, result), err
	}
	target := "refs/remotes/origin/" + options.To
	verdict, err := verifyProvenance(ctx, options.resolveGitHub(), options.Repository, walk, localInTarget(ctx, git, target))
	if err != nil {
		return result, err
	}
	result.Lanes, result.Unrecorded = verdict.Lanes, verdict.Unrecorded
	unrecorded := map[string]bool{}
	for _, commit := range verdict.Unrecorded {
		unrecorded[commit] = true
	}
	for _, merge := range verdict.Merges {
		part, err := mergeDelta(ctx, git, merge, unrecorded[merge.SHA])
		if err != nil {
			return result, err
		}
		if part != nil {
			result.Delta = append(result.Delta, *part)
		}
	}
	for _, fix := range verdict.Fixes {
		part, err := diffPart(ctx, git, BatchDeltaFix, fix, "", fix+"^", fix)
		if err != nil {
			return result, err
		}
		result.Delta = append(result.Delta, part)
	}
	if options.Overlap {
		parts, err := overlapParts(ctx, git, verdict)
		if err != nil {
			return result, err
		}
		result.Delta = append(result.Delta, parts...)
	}
	result.Outcome = LandSuccess
	switch {
	case len(verdict.Unrecorded) > 0:
		result.Outcome, result.RefusalCode = LandFindings, BatchRefusalUnrecordedMerge
		result.Reason = fmt.Sprintf("%d merge(s) bring in commits no still-valid review covers; their full diffs are below", len(verdict.Unrecorded))
	case len(result.Delta) == 0:
		result.Reason = "integration delta: empty (the promotion still needs a fresh review of its head)"
	default:
		result.Reason = fmt.Sprintf("integration delta: %d part(s)", len(result.Delta))
	}
	return result, nil
}

func derefPromote(refused *PromoteResult, fallback PromoteResult) PromoteResult {
	if refused != nil {
		return *refused
	}
	return fallback
}

// mergeDelta is a merge's resolution — its committed tree against the
// conflict-free merge of its parents — or, for an unrecorded merge, its whole
// diff against its first parent. A clean merge has no part.
func mergeDelta(ctx context.Context, git batchGit, merge integrationCommit, unrecorded bool) (*BatchDelta, error) {
	if unrecorded {
		part, err := diffPart(ctx, git, BatchDeltaUnrecorded, merge.SHA, "", merge.Parents[0], merge.SHA)
		return &part, err
	}
	tree, err := git.exec(ctx, configArgs(batchMergeOptions, "merge-tree", "--write-tree", merge.Parents[0], merge.Parents[1])...)
	if err != nil && tree.ExitCode != 1 {
		return nil, fmt.Errorf("git merge-tree %s: %w: %s", shortMergeRevision(merge.SHA), err, strings.TrimSpace(tree.Stderr))
	}
	mechanical, _, _ := strings.Cut(strings.TrimSpace(tree.Stdout), "\n")
	part, err := diffPart(ctx, git, BatchDeltaResolution, merge.SHA, "", mechanical, merge.SHA)
	if err != nil || part.Diff == "" {
		return nil, err
	}
	return &part, nil
}

func diffPart(ctx context.Context, git batchGit, kind, commit, lane string, revisions ...string) (BatchDelta, error) {
	names, err := git.out(ctx, append(append([]string{"diff", "--name-only"}, revisions...), "--")...)
	if err != nil {
		return BatchDelta{}, err
	}
	diff, err := git.out(ctx, append(append([]string{"diff"}, revisions...), "--")...)
	if err != nil {
		return BatchDelta{}, err
	}
	part := BatchDelta{Kind: kind, Commit: commit, Lane: lane, Diff: diff}
	if names != "" {
		part.Paths = strings.Split(names, "\n")
	}
	return part, nil
}

// overlapParts adds, for every file two or more lanes changed, each such
// lane's own diff of those files.
func overlapParts(ctx context.Context, git batchGit, verdict provenance) ([]BatchDelta, error) {
	firstParent := map[string]string{}
	for _, merge := range verdict.Merges {
		firstParent[merge.SHA] = merge.Parents[0]
	}
	changed := map[string][]string{}
	counts := map[string]int{}
	for i, lane := range verdict.Lanes {
		first := firstParent[lane.MergeCommit]
		names, err := git.out(ctx, "diff", "--name-only", first+"..."+lane.SHA, "--")
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Fields(names) {
			changed[strconv.Itoa(i)] = append(changed[strconv.Itoa(i)], name)
			counts[name]++
		}
	}
	var parts []BatchDelta
	for i, lane := range verdict.Lanes {
		var shared []string
		for _, name := range changed[strconv.Itoa(i)] {
			if counts[name] > 1 {
				shared = append(shared, name)
			}
		}
		if len(shared) == 0 {
			continue
		}
		sort.Strings(shared)
		first := firstParent[lane.MergeCommit]
		diff, err := git.out(ctx, append([]string{"diff", first + "..." + lane.SHA, "--"}, shared...)...)
		if err != nil {
			return nil, err
		}
		parts = append(parts, BatchDelta{Kind: BatchDeltaOverlap, Commit: lane.SHA, Lane: lane.Branch, Paths: shared, Diff: diff})
	}
	return parts, nil
}

// promoteBody is the promotion pull request's body: the marker, every
// recorded lane, and the commands to run next.
func promoteBody(options PromoteOptions, verdict provenance) string {
	var body strings.Builder
	body.WriteString(promoteMarker + "\n")
	fmt.Fprintf(&body, "Promotes `%s` into `%s`. Every lane below was reviewed at its exact commit and merged with its own merge commit.\n\n", options.Integration, options.To)
	body.WriteString("| Lane | Commit | Review | Reviewer |\n|---|---|---|---|\n")
	for _, lane := range verdict.Lanes {
		fmt.Fprintf(&body, "| `%s` | `%s` | %s | %s |\n", lane.Branch, lane.SHA, lane.Review, lane.Reviewer)
	}
	fmt.Fprintf(&body, "\nReview the integration delta of this pull request's head, even when it is empty:\n\n```sh\n%s --overlap\n```\n\n", options.deltaCommand())
	body.WriteString("Then land it with a review comment on this pull request ending `Reviewed-Head: <head>`:\n\n```sh\nwb pr land " + options.Repository + "#<number> --approved-by <review-comment-url>\n```\n")
	return body.String()
}

// PromoteIntegration is `wb integration promote`: re-prove the integration
// branch, then open, or refresh the body of, its one pull request to the
// promotion target.
func PromoteIntegration(ctx context.Context, options PromoteOptions) (PromoteResult, error) {
	options.verb = "integration promote"
	result := PromoteResult{SchemaVersion: 1, Verb: options.verb, Integration: options.Integration, To: options.To}
	git := options.checkoutGit()
	refused, walk, err := options.open(ctx, git, &result)
	if refused != nil || err != nil {
		return derefPromote(refused, result), err
	}
	if len(walk) == 0 {
		result.Outcome, result.Reason = LandSuccess, fmt.Sprintf("%s already contains %s; nothing to promote", options.To, options.Integration)
		return result, nil
	}
	github := options.resolveGitHub()
	verdict, err := verifyProvenance(ctx, github, options.Repository, walk, localInTarget(ctx, git, "refs/remotes/origin/"+options.To))
	if err != nil {
		return result, err
	}
	result.Lanes = verdict.Lanes
	if len(verdict.Unrecorded) > 0 {
		result.Unrecorded = verdict.Unrecorded
		return result.refuse(BatchRefusalUnrecordedMerge,
			fmt.Sprintf("merge %s brings in commits no still-valid review covers", shortMergeRevision(verdict.Unrecorded[0])), options.deltaCommand()), nil
	}
	body := promoteBody(options, verdict)
	number, existing, err := github.OpenPullRequest(ctx, options.Repository, options.Integration, options.To)
	if err != nil {
		return result, err
	}
	switch {
	case number == 0:
		if number, err = github.CreatePullRequest(ctx, options.Repository, options.Integration, options.To, fmt.Sprintf("Promote %s to %s", options.Integration, options.To), body); err != nil {
			return result, err
		}
		result.Changed = true
	case existing != body:
		if err := github.UpdatePullRequestBody(ctx, options.Repository, number, body); err != nil {
			return result, err
		}
		result.Changed = true
	}
	result.PullRequest = options.Repository + "#" + strconv.Itoa(number)
	result.Outcome = LandSuccess
	result.Reason = fmt.Sprintf("%s promotes %d reviewed lane(s) into %s", result.PullRequest, len(verdict.Lanes), options.To)
	result.Command = options.deltaCommand() + " --overlap"
	return result, nil
}
