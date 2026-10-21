package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/progress"
)

// A target whose active rules include a merge_queue rule is landed by GitHub's
// merge queue, never by WB. The queue builds a merge group from the current
// target plus the queued pull requests, runs the required checks on that
// group, and merges only what passed. A direct merge call skips all of that:
// GitHub's synchronous merge endpoint does not support merge queues, and a
// caller the ruleset lets bypass (an organization admin with bypass mode
// "always") can land a head that was never tested on top of the current
// target. So on a queue target WB waits for the pull request's own required
// checks, enqueues the exact head it observed, and watches the queue until the
// pull request is merged, removed, or the wait budget runs out. It never arms
// auto-merge, never updates the branch, and never calls a merge endpoint.

// LandRefusal codes for merge-queue targets.
const (
	// LandRefusalMergeQueueOption reports a flag the target's merge queue
	// overrides: the queue merges with its own method and commit message.
	LandRefusalMergeQueueOption = "merge-queue-option-conflict"
	// LandRefusalMergeQueueEnqueue reports that GitHub refused to add the
	// pull request to the merge queue.
	LandRefusalMergeQueueEnqueue = "merge-queue-enqueue-rejected"
	// LandRefusalMergeGroupFailed reports that the queue removed the pull
	// request because its merge group's checks failed or timed out.
	LandRefusalMergeGroupFailed = "merge-group-failed"
	// LandRefusalMergeQueueDequeued reports that the pull request left the
	// queue unmerged for any other reason, such as a manual removal.
	LandRefusalMergeQueueDequeued = "merge-queue-dequeued"
	// LandRefusalMergeQueuePending reports that the wait budget ran out while
	// the pull request was still queued. GitHub merges it without WB if its
	// merge group passes; the resume command verifies and finishes the tail.
	LandRefusalMergeQueuePending = "merge-queue-pending"
)

// MergeQueueReceipt records what the merge queue was asked to do and what it
// was last seen doing.
type MergeQueueReceipt struct {
	RulesetID   int64  `json:"ruleset_id"`
	MergeMethod string `json:"merge_method"`
	// Enqueued is true when this invocation added the pull request; a resumed
	// landing that found it already queued leaves it false.
	Enqueued      bool     `json:"enqueued"`
	EntryState    string   `json:"entry_state,omitempty"`
	Position      int      `json:"position,omitempty"`
	MergeGroupSHA string   `json:"merge_group_sha,omitempty"`
	RemovalReason string   `json:"removal_reason,omitempty"`
	FailedChecks  []string `json:"failed_checks,omitempty"`
}

func newMergeQueueReceipt(policy githubchecks.MergeQueuePolicy) *MergeQueueReceipt {
	return &MergeQueueReceipt{RulesetID: policy.RulesetID, MergeMethod: policy.MergeMethod}
}

// mergeQueueOptionConflict refuses a flag whose effect the queue would
// silently discard: the queue merges the pull request head itself, with the
// method and commit message its own configuration names.
func mergeQueueOptionConflict(options PullRequestLandOptions, policy githubchecks.MergeQueuePolicy, target, number string) *landRefusal {
	var conflict string
	switch {
	case len(options.KeepCommits) > 0:
		conflict = "--keep-commits rebuilds the branch for a direct merge"
	case strings.TrimSpace(options.Subject) != "":
		conflict = "--subject writes a squash commit message WB would send with a direct merge"
	case options.MergeMethodExplicit && options.MergeMethod != policy.MergeMethod:
		conflict = "--merge-method " + options.MergeMethod + " differs from the queue's own method"
	default:
		return nil
	}
	stripped := options
	stripped.KeepCommits, stripped.Reason, stripped.Subject, stripped.MergeMethodExplicit = nil, "", "", false
	return &landRefusal{
		code: LandRefusalMergeQueueOption,
		reason: fmt.Sprintf("%s requires merge queue ruleset %d, which merges the pull request head itself with its own method (%s); %s",
			target, policy.RulesetID, policy.MergeMethod, conflict),
		command: pullRequestLandResumeCommand(stripped, number, ""),
	}
}

// mergeQueueState is one GraphQL observation of a pull request's place in
// its base branch's merge queue.
type mergeQueueState struct {
	ID             string
	State          string
	Merged         bool
	HeadSHA        string
	MergeCommitSHA string
	// HeadCommittedAt is the head commit's committer date, which tells a head
	// pushed after the last removal from the head that removal was about.
	HeadCommittedAt string
	InQueue         bool
	EntryState      string
	Position        int
	MergeGroupSHA   string
	// LastQueueEvent is the type of the newest added/removed merge-queue
	// timeline event. When it is a removal, RemovalReason is GitHub's token
	// for it (merged, failed_checks, manual, ...), RemovedAt its time and
	// RemovalGroupSHA the merge-group commit the queue had built.
	LastQueueEvent  string
	RemovalReason   string
	RemovedAt       string
	RemovalGroupSHA string
}

const removedFromMergeQueueEvent = "RemovedFromMergeQueueEvent"

const mergeQueueStateQuery = "query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){" +
	"id state merged headRefOid isInMergeQueue mergeCommit{oid} commits(last:1){nodes{commit{committedDate}}} mergeQueueEntry{state position headCommit{oid}} " +
	"timelineItems(last:1,itemTypes:[ADDED_TO_MERGE_QUEUE_EVENT,REMOVED_FROM_MERGE_QUEUE_EVENT]){nodes{__typename ... on RemovedFromMergeQueueEvent{reason createdAt beforeCommit{oid}}}}}}}"

type graphQLCommitRef struct {
	OID string `json:"oid"`
}

func readMergeQueueState(ctx context.Context, repository, number string) (mergeQueueState, error) {
	owner, name, _ := strings.Cut(repository, "/")
	output, err := githubRead(ctx, "", "api", "graphql",
		"-f", "query="+mergeQueueStateQuery, "-f", "owner="+owner, "-f", "name="+name, "-F", "number="+number)
	if err != nil {
		return mergeQueueState{}, fmt.Errorf("read merge queue state of %s#%s: %w", repository, number, err)
	}
	var decoded struct {
		Data struct {
			Repository struct {
				PullRequest *struct {
					ID             string            `json:"id"`
					State          string            `json:"state"`
					Merged         bool              `json:"merged"`
					HeadRefOID     string            `json:"headRefOid"`
					IsInMergeQueue bool              `json:"isInMergeQueue"`
					MergeCommit    *graphQLCommitRef `json:"mergeCommit"`
					Commits        struct {
						Nodes []struct {
							Commit struct {
								CommittedDate string `json:"committedDate"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
					MergeQueueEntry *struct {
						State      string            `json:"state"`
						Position   int               `json:"position"`
						HeadCommit *graphQLCommitRef `json:"headCommit"`
					} `json:"mergeQueueEntry"`
					TimelineItems struct {
						Nodes []struct {
							TypeName     string            `json:"__typename"`
							Reason       string            `json:"reason"`
							CreatedAt    string            `json:"createdAt"`
							BeforeCommit *graphQLCommitRef `json:"beforeCommit"`
						} `json:"nodes"`
					} `json:"timelineItems"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		return mergeQueueState{}, fmt.Errorf("decode merge queue state of %s#%s: %w", repository, number, err)
	}
	view := decoded.Data.Repository.PullRequest
	if view == nil || strings.TrimSpace(view.ID) == "" {
		return mergeQueueState{}, fmt.Errorf("merge queue state of %s#%s carried no pull request", repository, number)
	}
	state := mergeQueueState{ID: view.ID, State: view.State, Merged: view.Merged, HeadSHA: view.HeadRefOID, InQueue: view.IsInMergeQueue}
	if view.MergeCommit != nil {
		state.MergeCommitSHA = view.MergeCommit.OID
	}
	if commits := view.Commits.Nodes; len(commits) > 0 {
		state.HeadCommittedAt = commits[len(commits)-1].Commit.CommittedDate
	}
	if entry := view.MergeQueueEntry; entry != nil {
		state.InQueue = true
		state.EntryState, state.Position = entry.State, entry.Position
		if entry.HeadCommit != nil {
			state.MergeGroupSHA = entry.HeadCommit.OID
		}
	}
	if nodes := view.TimelineItems.Nodes; len(nodes) > 0 {
		last := nodes[len(nodes)-1]
		state.LastQueueEvent = last.TypeName
		if last.TypeName == removedFromMergeQueueEvent {
			state.RemovalReason, state.RemovedAt = strings.TrimSpace(last.Reason), last.CreatedAt
			if last.BeforeCommit != nil {
				state.RemovalGroupSHA = last.BeforeCommit.OID
			}
		}
	}
	return state, nil
}

// Merge-queue removal kinds, from GitHub's removal reason token.
const (
	mergeQueueRemovedMerging = "merging"
	mergeQueueRemovedFailed  = "failed"
	mergeQueueRemovedOther   = "dequeued"
)

// mergeQueueRemovalKind classifies a removal. GitHub records the queue's own
// successful merge as a removal with reason "merged" a moment before the pull
// request reports itself merged, so that removal means "wait for the merge",
// never "dequeued".
func mergeQueueRemovalKind(reason string) string {
	token := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case token == "merged" || token == "already_merged":
		return mergeQueueRemovedMerging
	case token == "failed_checks" || strings.Contains(token, "timeout") || strings.Contains(token, "timed_out"):
		return mergeQueueRemovedFailed
	}
	return mergeQueueRemovedOther
}

// removed reports the newest queue event as a removal of the given kind.
func (state mergeQueueState) removed(kind string) bool {
	return !state.InQueue && state.LastQueueEvent == removedFromMergeQueueEvent && mergeQueueRemovalKind(state.RemovalReason) == kind
}

// headPredatesRemoval reports that the newest removal was about the current
// head: nothing was committed after it. An unreadable date counts as "same
// head", so WB asks before retrying rather than retrying silently.
func (state mergeQueueState) headPredatesRemoval() bool {
	removedAt, removedErr := time.Parse(time.RFC3339, state.RemovedAt)
	committedAt, committedErr := time.Parse(time.RFC3339, state.HeadCommittedAt)
	return removedErr != nil || committedErr != nil || !committedAt.After(removedAt)
}

const mergeQueueEnqueueMutation = "mutation($id:ID!,$head:GitObjectID!){enqueuePullRequest(input:{pullRequestId:$id,expectedHeadOid:$head}){mergeQueueEntry{state position}}}"

// enqueuePullRequest adds the pull request to its merge queue, pinned to the
// exact head WB observed green: GitHub rejects the call if the head moved. It
// records the entry GitHub returns on the receipt.
func enqueuePullRequest(ctx context.Context, repository, number, nodeID, head string, receipt *MergeQueueReceipt) (*landRefusal, error) {
	response := githubExecute(ctx, "", "api", "graphql",
		"-f", "query="+mergeQueueEnqueueMutation, "-f", "id="+nodeID, "-f", "head="+head)
	if response.Err == nil {
		var decoded struct {
			Data struct {
				EnqueuePullRequest struct {
					MergeQueueEntry *struct {
						State    string `json:"state"`
						Position int    `json:"position"`
					} `json:"mergeQueueEntry"`
				} `json:"enqueuePullRequest"`
			} `json:"data"`
		}
		if json.Unmarshal(response.Stdout, &decoded) == nil && decoded.Data.EnqueuePullRequest.MergeQueueEntry != nil {
			entry := decoded.Data.EnqueuePullRequest.MergeQueueEntry
			receipt.EntryState, receipt.Position = entry.State, entry.Position
		}
		return nil, nil
	}
	message := githubobserver.CommandDiagnostic(response)
	if githubobserver.IsTransientCommandFailure(ctx, response) {
		return nil, fmt.Errorf("%w: enqueue %s#%s at %s: %s",
			githubobserver.ErrTransientMutationOutcomeUnknown, repository, number, shortMergeRevision(head), message)
	}
	return &landRefusal{
		code:    LandRefusalMergeQueueEnqueue,
		reason:  "GitHub refused to add the pull request to the merge queue: " + message,
		command: "gh pr view " + number + " --repo " + repository + " --web",
	}, nil
}

// mergeQueueOutcome is how one enqueue-and-observe pass ended. Exactly one of
// merged, pending and refusal is set; findings marks a refusal that is a
// verdict on the change (the queue removed it) rather than on the request.
type mergeQueueOutcome struct {
	merged   bool
	pending  bool
	refusal  *landRefusal
	findings bool
	reason   string
}

// recordMergeQueueEntry copies one observation of the queue entry onto the
// receipt.
func recordMergeQueueEntry(receipt *MergeQueueReceipt, state mergeQueueState) {
	if state.InQueue {
		receipt.EntryState, receipt.Position = state.EntryState, state.Position
	}
	if state.MergeGroupSHA != "" {
		receipt.MergeGroupSHA = state.MergeGroupSHA
	}
}

// enqueueAndAwaitMergeQueue enqueues head (unless it is already queued) and
// observes the queue until deadline. It is the only landing write WB makes on
// a merge-queue target.
func enqueueAndAwaitMergeQueue(ctx context.Context, options PullRequestLandOptions, number, head, target string, deadline time.Time, receipt *MergeQueueReceipt) (mergeQueueOutcome, error) {
	state, err := readMergeQueueState(ctx, options.Repository, number)
	if err != nil {
		return mergeQueueOutcome{}, err
	}
	if outcome, done := judgeMergeQueueState(state, options, number, head, false); done {
		return outcome, nil
	}
	recordMergeQueueEntry(receipt, state)
	// Already queued, or removed by the queue's own merge that GitHub has not
	// yet reported as merged: observe, never enqueue a second time.
	seenQueued := state.InQueue || state.removed(mergeQueueRemovedMerging)
	if !seenQueued {
		if state.LastQueueEvent == removedFromMergeQueueEvent && !options.Requeue && state.headPredatesRemoval() {
			// The queue already removed this exact head while nobody was
			// watching. Enqueuing it again would re-run the same merge group
			// on every resume; report the removal and let the caller choose.
			return mergeQueueRemoval(ctx, options, number, target, state, receipt), nil
		}
		reportPullRequestLandProgress(options.OperationProgress, "enqueue_pull_request", progress.Started, shortMergeRevision(head), 0, 0)
		refusal, enqueueErr := enqueuePullRequest(ctx, options.Repository, number, state.ID, head, receipt)
		if refusal != nil {
			return mergeQueueOutcome{refusal: refusal}, nil
		}
		if enqueueErr != nil {
			// The write may have landed before its response was lost: re-read
			// rather than enqueue twice, and keep the unknown outcome resumable
			// unless the read proves the exact head is queued or merged.
			recheck, readErr := readMergeQueueState(ctx, options.Repository, number)
			if readErr != nil || recheck.HeadSHA != head || !recheck.InQueue && !recheck.Merged {
				return mergeQueueOutcome{}, enqueueErr
			}
		}
		receipt.Enqueued, seenQueued = true, true
		reportPullRequestLandProgress(options.OperationProgress, "enqueue_pull_request", progress.Completed, shortMergeRevision(head), 0, 0)
	}

	// From here the queue serializes the target, so a lane held for the rest
	// of this wait would only refuse other landings the queue could batch.
	stopLaneHeartbeat := func() {}
	if options.releaseLane != nil {
		options.releaseLane()
	} else {
		stopLaneHeartbeat = startLandingLaneHeartbeat(options.ProjectsRoot, options.Repository, target, options.Lane.Owner.WBSessionID, 0)
	}
	defer stopLaneHeartbeat()
	interval := options.CheckPollInterval
	if interval <= 0 {
		interval = githubchecks.DefaultCheckPollInterval
	}
	for {
		if remainingWaitBudget(options, deadline) <= interval {
			return mergeQueueOutcome{pending: true, reason: mergeQueuePendingReason(target, *receipt)}, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return mergeQueueOutcome{}, ctx.Err()
		case <-timer.C:
		}
		state, err = readMergeQueueState(ctx, options.Repository, number)
		if err != nil {
			return mergeQueueOutcome{}, err
		}
		recordMergeQueueEntry(receipt, state)
		if state.InQueue {
			reportPullRequestLandProgress(options.OperationProgress, "merge_queue", progress.Waiting,
				fmt.Sprintf("%s, position %d", strings.ToLower(state.EntryState), state.Position), 0, 0)
		}
		if outcome, done := judgeMergeQueueState(state, options, number, head, seenQueued); done {
			if outcome.findings {
				outcome = mergeQueueRemoval(ctx, options, number, target, state, receipt)
			}
			return outcome, nil
		}
	}
}

// judgeMergeQueueState decides whether one observation ends the landing. A
// removal counts only once the pull request has been seen queued, so a read
// that predates GitHub reporting the new entry is not mistaken for one, and a
// removal by the queue's own merge only ever waits for the merge.
func judgeMergeQueueState(state mergeQueueState, options PullRequestLandOptions, number, head string, seenQueued bool) (mergeQueueOutcome, bool) {
	switch {
	case state.Merged:
		return mergeQueueOutcome{merged: true}, true
	case !strings.EqualFold(state.State, "open"):
		return mergeQueueOutcome{refusal: &landRefusal{
			code:    LandRefusalNotOpen,
			reason:  "pull request state is " + state.State + " and it was not merged by the merge queue",
			command: "gh pr view " + number + " --repo " + options.Repository + " --web",
		}}, true
	case state.HeadSHA != head:
		return mergeQueueOutcome{refusal: &landRefusal{
			code:    LandRefusalHeadMoved,
			reason:  fmt.Sprintf("the pull request head moved from %s to %s, which takes it out of the merge queue", shortMergeRevision(head), shortMergeRevision(state.HeadSHA)),
			command: pullRequestLandResumeCommand(options, number, ""),
		}}, true
	case seenQueued && (state.removed(mergeQueueRemovedFailed) || state.removed(mergeQueueRemovedOther)):
		return mergeQueueOutcome{findings: true}, true
	}
	return mergeQueueOutcome{}, false
}

func mergeQueuePendingReason(target string, receipt MergeQueueReceipt) string {
	where := "GitHub has not yet reported its place in the queue"
	if receipt.EntryState != "" {
		where = fmt.Sprintf("last seen %s at position %d", strings.ToLower(receipt.EntryState), receipt.Position)
	}
	return fmt.Sprintf("the pull request is in %s's merge queue (%s) and the landing wait budget elapsed before the queue merged it; GitHub merges it without WB if its merge group passes", target, where)
}

// mergeQueueRemoval reports the removal the newest timeline event records,
// naming the checks that failed on the merge group the queue had built.
func mergeQueueRemoval(ctx context.Context, options PullRequestLandOptions, number, target string, state mergeQueueState, receipt *MergeQueueReceipt) mergeQueueOutcome {
	receipt.RemovalReason = state.RemovalReason
	if state.RemovalGroupSHA != "" {
		receipt.MergeGroupSHA = state.RemovalGroupSHA
	}
	receipt.FailedChecks = mergeGroupFailedChecks(ctx, options, target, receipt.MergeGroupSHA)
	return mergeQueueOutcome{findings: true, refusal: mergeQueueRemovalRefusal(options, number, target, *receipt)}
}

// mergeGroupFailedChecks names the checks that failed on the merge group the
// queue last built, so a removal can say what broke. A read failure leaves the
// list empty; the removal reason GitHub recorded still stands.
func mergeGroupFailedChecks(ctx context.Context, options PullRequestLandOptions, target, mergeGroupSHA string) []string {
	if mergeGroupSHA == "" {
		return nil
	}
	checks, _, reason := options.resolveInspector().CommitChecks(ctx, githubchecks.PullRequestWaitOptions{
		Repository: options.Repository, Target: target, Head: mergeGroupSHA,
	})
	if reason != "" {
		return nil
	}
	var failed []string
	for _, check := range checks {
		if check.Bucket == "fail" || check.Bucket == "cancel" {
			failed = append(failed, check.Name)
		}
	}
	return failed
}

func mergeQueueRemovalRefusal(options PullRequestLandOptions, number, target string, receipt MergeQueueReceipt) *landRefusal {
	why := receipt.RemovalReason
	if why == "" {
		why = "no reason recorded"
	}
	reason := fmt.Sprintf("GitHub's merge queue on %s removed the pull request without merging it (%s)", target, why)
	code := LandRefusalMergeQueueDequeued
	if mergeQueueRemovalKind(receipt.RemovalReason) == mergeQueueRemovedFailed {
		code = LandRefusalMergeGroupFailed
	}
	if len(receipt.FailedChecks) > 0 {
		reason += "; merge group " + shortMergeRevision(receipt.MergeGroupSHA) + " failed " + strings.Join(receipt.FailedChecks, ", ")
	}
	requeue := options
	requeue.Requeue = true
	return &landRefusal{
		code:    code,
		reason:  reason + "; push a fix to enqueue the new head, or re-run with --requeue to retry this same head",
		command: pullRequestLandResumeCommand(requeue, number, prLandResumeTimeoutFlag(options.Slice)),
	}
}

// landThroughMergeQueue is `wb pr land`'s tail on a merge-queue target: it
// replaces the merge call, then hands a merged pull request to the same
// verification and cleanup every other landing finishes with.
func landThroughMergeQueue(ctx context.Context, options PullRequestLandOptions, result PullRequestLandResult, view githubchecks.PullRequestView, number string, deadline time.Time) (PullRequestLandResult, error) {
	result.ManualEquivalent[3] = "gh api graphql -f query='mutation{enqueuePullRequest(…)}'  (then poll the merge queue until it merges)"
	outcome, err := enqueueAndAwaitMergeQueue(ctx, options, number, view.Head.SHA, view.Base.Ref, deadline, result.MergeQueue)
	if err != nil {
		// LandPullRequest appends the resume command to a transient failure.
		return result, err
	}
	switch {
	case outcome.pending:
		result.Outcome = LandFindings
		result.RefusalCode = LandRefusalMergeQueuePending
		result.Reason = outcome.reason
		result.SanctionedCommand = pullRequestLandResumeCommand(options, number, prLandResumeTimeoutFlag(options.Slice))
		return withSavings(result), nil
	case outcome.refusal != nil:
		result = mergeRefusal(result, *outcome.refusal)
		if outcome.findings {
			result.Outcome = LandFindings
		}
		return result, nil
	}
	result.Evidence["merged_by"] = "github merge queue"
	return finalizeLandedPullRequest(ctx, options, result, view, view.Head.SHA, number)
}

// mergeQueueEvidence is the receipt line that says why this landing did not
// merge directly.
func mergeQueueEvidence(policy githubchecks.MergeQueuePolicy, target string) string {
	return target + " requires merge queue ruleset " + strconv.FormatInt(policy.RulesetID, 10) +
		" (" + policy.MergeMethod + "); WB enqueues the pull request and never merges it directly"
}
