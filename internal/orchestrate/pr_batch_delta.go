package orchestrate

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

// Delta part kinds.
const (
	BatchDeltaResolution = "resolution"
	BatchDeltaFix        = "fix"
	BatchDeltaUnrecorded = "unrecorded"
	BatchDeltaOverlap    = "overlap"
)

// BatchDelta is one part of the integration delta.
type BatchDelta struct {
	Kind        string   `json:"kind"`
	Commit      string   `json:"commit,omitempty"`
	PullRequest int      `json:"pr,omitempty"`
	Paths       []string `json:"paths"`
	Diff        string   `json:"diff"`
}

// batchView is a verified batch: its state, published head and target tip.
type batchView struct {
	state     BatchState
	head      string
	targetRef string
	number    int
}

// openBatch loads the batch for the worktree, fetches origin, and applies
// REQ batch-merge-provenance's base check.
func (options BatchOptions) openBatch(ctx context.Context, git batchGit, result BatchResult) (batchView, *BatchResult, error) {
	branch, issue := options.checkBatchWorktree(ctx, git)
	if issue != nil {
		refused := refuseIssue(result, *issue)
		return batchView{}, &refused, nil
	}
	state, number, _, err := options.loadPublishedState(ctx, branch)
	if err != nil {
		return batchView{}, nil, err
	}
	if state.Version == 0 {
		refused := result.refuse(BatchRefusalNotFound, "no batch state for "+branch, "wb pr batch create")
		return batchView{}, &refused, nil
	}
	view := batchView{state: state, targetRef: "origin/" + state.Target, number: number}
	if _, err := git.out(ctx, "fetch", "--quiet", "origin", state.Target); err != nil {
		return batchView{}, nil, err
	}
	view.head, err = git.out(ctx, "rev-parse", "HEAD")
	if err != nil {
		return batchView{}, nil, err
	}
	if number != 0 {
		if _, err := git.out(ctx, "fetch", "--quiet", "origin", state.Branch); err != nil {
			return batchView{}, nil, err
		}
		if view.head, err = git.out(ctx, "rev-parse", "origin/"+state.Branch); err != nil {
			return batchView{}, nil, err
		}
	}
	if onTarget, err := git.test(ctx, "merge-base", "--is-ancestor", state.Base, view.targetRef); err != nil {
		return batchView{}, nil, err
	} else if !onTarget {
		refused := result.refuse(BatchRefusalBaseInvalid, fmt.Sprintf("recorded base %s is not on %s", shortMergeRevision(state.Base), view.targetRef), "")
		return batchView{}, &refused, nil
	}
	return view, nil, nil
}

// recordedSource reports whether parent is a recorded pin whose review still
// resolves with the recorded digest and Reviewed-Head.
func (options BatchOptions) recordedSource(ctx context.Context, state BatchState, parent string) (BatchStep, bool) {
	for _, step := range state.sources() {
		if step.Head != parent {
			continue
		}
		body, err := options.resolveGitHub().IssueComment(ctx, state.Repository, step.ReviewCommentID)
		if err != nil || ReviewDigest(body) != step.ReviewDigest {
			return step, false
		}
		reviewed, err := parseReviewedHeadLine(body)
		return step, err == nil && reviewed == parent
	}
	return BatchStep{}, false
}

// diffPart diffs revisions (one range, or two endpoints) limited to paths.
func (git batchGit) diffPart(ctx context.Context, kind, commit string, revisions []string, paths ...string) (BatchDelta, error) {
	spec := append(append(append([]string(nil), revisions...), "--"), paths...)
	diff, err := git.out(ctx, append([]string{"diff", "--no-color"}, spec...)...)
	if err != nil {
		return BatchDelta{}, err
	}
	names, err := git.out(ctx, append([]string{"diff", "--name-only"}, spec...)...)
	if err != nil {
		return BatchDelta{}, err
	}
	part := BatchDelta{Kind: kind, Commit: commit, Paths: []string{}, Diff: diff}
	if names != "" {
		part.Paths = strings.Split(names, "\n")
	}
	return part, nil
}

// DeltaPullRequestBatch is `wb pr batch delta`: only what the batch itself
// introduced — conflict resolutions, fix commits, and any merge WB cannot
// account for — plus, with Overlap, each source's diff of files more than one
// source changed.
func DeltaPullRequestBatch(ctx context.Context, options BatchOptions) (BatchResult, error) {
	result := BatchResult{SchemaVersion: 1, Verb: "pr batch delta", Worktree: options.Worktree}
	git := options.git()
	view, refused, err := options.openBatch(ctx, git, result)
	if err != nil || refused != nil {
		if refused != nil {
			return *refused, nil
		}
		return result, err
	}
	state := view.state
	result.State, result.Head = &state, view.head
	list, err := git.out(ctx, "rev-list", "--first-parent", "--reverse", state.Base+".."+view.head)
	if err != nil {
		return result, err
	}
	var unrecorded []string
	for _, commit := range strings.Fields(list) {
		line, err := git.out(ctx, "rev-list", "--parents", "-n", "1", commit)
		if err != nil {
			return result, err
		}
		parents := strings.Fields(line)[1:]
		if len(parents) == 1 {
			part, err := git.diffPart(ctx, BatchDeltaFix, commit, []string{parents[0], commit})
			if err != nil {
				return result, err
			}
			result.Delta = append(result.Delta, part)
			continue
		}
		_, recorded := options.recordedSource(ctx, state, parents[1])
		if !recorded {
			refresh, err := git.test(ctx, "merge-base", "--is-ancestor", parents[1], view.targetRef)
			if err != nil {
				return result, err
			}
			recorded = refresh
		}
		if !recorded {
			part, err := git.diffPart(ctx, BatchDeltaUnrecorded, commit, []string{parents[0], commit})
			if err != nil {
				return result, err
			}
			result.Delta = append(result.Delta, part)
			unrecorded = append(unrecorded, shortMergeRevision(commit))
			continue
		}
		mechanical, err := git.exec(ctx, configArgs(state.MergeOptions, "merge-tree", "--write-tree", "--no-messages", parents[0], parents[1])...)
		tree := strings.TrimSpace(strings.SplitN(mechanical.Stdout, "\n", 2)[0])
		if tree == "" {
			return result, fmt.Errorf("merge-tree %s %s: %v: %s", shortMergeRevision(parents[0]), shortMergeRevision(parents[1]), err, strings.TrimSpace(mechanical.Stderr))
		}
		part, err := git.diffPart(ctx, BatchDeltaResolution, commit, []string{tree, commit})
		if err != nil {
			return result, err
		}
		if part.Diff != "" {
			result.Delta = append(result.Delta, part)
		}
	}
	if options.Overlap {
		overlap, err := git.overlapParts(ctx, state)
		if err != nil {
			return result, err
		}
		result.Delta = append(result.Delta, overlap...)
	}
	if len(unrecorded) > 0 {
		return result.finding(BatchRefusalUnrecordedMerge, "merge commits WB did not record or whose review changed: "+strings.Join(unrecorded, ", "), ""), nil
	}
	result.Outcome = LandSuccess
	result.Reason = "integration delta: " + strconv.Itoa(countIntegrationParts(result.Delta)) + " part(s)"
	if countIntegrationParts(result.Delta) == 0 {
		result.Reason = "integration delta: empty"
	}
	return result, nil
}

func countIntegrationParts(parts []BatchDelta) int {
	count := 0
	for _, part := range parts {
		if part.Kind != BatchDeltaOverlap {
			count++
		}
	}
	return count
}

// overlapParts returns, for every file changed by two or more sources, each
// such source's own diff of those files.
func (git batchGit) overlapParts(ctx context.Context, state BatchState) ([]BatchDelta, error) {
	touched := map[string][]int{}
	sources := state.sources()
	for i, source := range sources {
		names, err := git.out(ctx, "diff", "--name-only", state.Base+"..."+source.Head)
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Fields(names) {
			touched[name] = append(touched[name], i)
		}
	}
	shared := map[int]map[string]bool{}
	for name, owners := range touched {
		if len(owners) < 2 {
			continue
		}
		for _, owner := range owners {
			if shared[owner] == nil {
				shared[owner] = map[string]bool{}
			}
			shared[owner][name] = true
		}
	}
	var parts []BatchDelta
	for i, source := range sources {
		if shared[i] == nil {
			continue
		}
		part, err := git.diffPart(ctx, BatchDeltaOverlap, source.Head, []string{state.Base + "..." + source.Head}, sortedKeys(shared[i])...)
		if err != nil {
			return nil, err
		}
		part.PullRequest = source.PullRequest
		parts = append(parts, part)
	}
	return parts, nil
}

// Attribution classes (REQ batch-failure-attribution).
const (
	BatchFailurePreExisting  = "pre-existing"
	BatchFailureSource       = "source"
	BatchFailureIntegration  = "integration"
	BatchFailureUnattributed = "unattributed"
)

// BatchFailed attributes one failed check on the batch head.
type BatchFailed struct {
	Check   string `json:"check"`
	Class   string `json:"class"`
	Sources []int  `json:"sources,omitempty"`
}

// String renders `check: class` with any sources.
func (failed BatchFailed) String() string {
	if failed.Class != BatchFailureSource {
		return failed.Check + ": " + failed.Class
	}
	labels := make([]string, 0, len(failed.Sources))
	for _, source := range failed.Sources {
		labels = append(labels, "#"+strconv.Itoa(source))
	}
	return failed.Check + ": source " + strings.Join(labels, ", ")
}

// StatusPullRequestBatch is `wb pr batch status`: the batch's steps and an
// attribution of every failed check on its head, from existing runs only.
func StatusPullRequestBatch(ctx context.Context, options BatchOptions) (BatchResult, error) {
	result := BatchResult{SchemaVersion: 1, Verb: "pr batch status", Worktree: options.Worktree}
	git := options.git()
	view, refused, err := options.openBatch(ctx, git, result)
	if err != nil || refused != nil {
		if refused != nil {
			return *refused, nil
		}
		return result, err
	}
	state := view.state
	result.State, result.Head = &state, view.head
	if view.number != 0 {
		result.PullRequest = state.Repository + "#" + strconv.Itoa(view.number)
	}
	github := options.resolveGitHub()
	cache := map[string][]githubchecks.RemoteCheck{}
	checksAt := func(sha string) ([]githubchecks.RemoteCheck, error) {
		if checks, ok := cache[sha]; ok {
			return checks, nil
		}
		checks, err := github.CommitChecks(ctx, state.Repository, sha)
		cache[sha] = checks
		return checks, err
	}
	headChecks, err := checksAt(view.head)
	if err != nil {
		return result, err
	}
	for _, check := range headChecks {
		if check.Bucket != "fail" && check.Bucket != "cancel" {
			continue
		}
		failed, err := attribute(check.Name, state, checksAt)
		if err != nil {
			return result, err
		}
		result.Attribution = append(result.Attribution, failed)
	}
	result.Outcome = LandSuccess
	result.Reason = fmt.Sprintf("%d step(s); %d failed check(s) on %s", len(state.Steps), len(result.Attribution), shortMergeRevision(view.head))
	return result, nil
}

// checkState returns "fail", "pass", or "" when the run is absent or still
// running.
func checkState(checks []githubchecks.RemoteCheck, name string) string {
	normalized := githubchecks.NormalizeCheckName(name)
	for _, check := range checks {
		if githubchecks.NormalizeCheckName(check.Name) != normalized {
			continue
		}
		switch check.Bucket {
		case "pass", "skipping":
			return "pass"
		case "fail", "cancel":
			return "fail"
		}
		return ""
	}
	return ""
}

func attribute(name string, state BatchState, checksAt func(string) ([]githubchecks.RemoteCheck, error)) (BatchFailed, error) {
	failed := BatchFailed{Check: githubchecks.NormalizeCheckName(name)}
	baseChecks, err := checksAt(state.Base)
	if err != nil {
		return failed, err
	}
	base := checkState(baseChecks, name)
	if base == "fail" {
		failed.Class = BatchFailurePreExisting
		return failed, nil
	}
	known := base != ""
	for _, source := range state.sources() {
		checks, err := checksAt(source.Head)
		if err != nil {
			return failed, err
		}
		switch checkState(checks, name) {
		case "fail":
			failed.Sources = append(failed.Sources, source.PullRequest)
		case "":
			known = false
		}
	}
	switch {
	case len(failed.Sources) > 0 && base == "pass":
		failed.Class = BatchFailureSource
	case known:
		failed.Class = BatchFailureIntegration
	default:
		failed.Class, failed.Sources = BatchFailureUnattributed, nil
	}
	return failed, nil
}
