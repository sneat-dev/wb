package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

const queueHead = "1111111111111111111111111111111111111111"

// queueRead is one scripted answer to the merge-queue state query.
type queueRead struct {
	state, head, entryState, group, event, reason string
	removedAt, removedGroup, committedAt, suiteAt string
	merged, inQueue                               bool
	position                                      int
	fail                                          error
	raw                                           string
}

func (read queueRead) body(t *testing.T) string {
	t.Helper()
	if read.raw != "" {
		return read.raw
	}
	state, head := read.state, read.head
	if state == "" {
		state = "OPEN"
	}
	if head == "" {
		head = queueHead
	}
	pull := map[string]any{"id": "PR_node", "state": state, "merged": read.merged, "headRefOid": head, "isInMergeQueue": read.inQueue}
	if read.merged {
		pull["mergeCommit"] = map[string]string{"oid": "merged-sha"}
	}
	if read.inQueue {
		entry := map[string]any{"state": read.entryState, "position": read.position}
		if read.group != "" {
			entry["headCommit"] = map[string]string{"oid": read.group}
		}
		pull["mergeQueueEntry"] = entry
	}
	nodes := []map[string]any{}
	if read.event != "" {
		node := map[string]any{"__typename": read.event, "reason": read.reason, "createdAt": read.removedAt}
		if read.removedGroup != "" {
			node["beforeCommit"] = map[string]string{"oid": read.removedGroup}
		}
		nodes = append(nodes, node)
	}
	pull["timelineItems"] = map[string]any{"nodes": nodes}
	if read.committedAt != "" {
		commit := map[string]any{"committedDate": read.committedAt}
		if read.suiteAt != "" {
			commit["checkSuites"] = map[string]any{"nodes": []map[string]string{{"createdAt": read.suiteAt}}}
		}
		pull["commits"] = map[string]any{"nodes": []map[string]any{{"commit": commit}}}
	}
	encoded, err := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": pull}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// queueScript is a scripted GitHub for one merge-queue landing. Reads are
// consumed in order and the last one repeats; every write is recorded, and
// any write other than the exact pinned enqueue fails the test.
type queueScript struct {
	t       *testing.T
	mu      sync.Mutex
	reads   []queueRead
	enqueue githubobserver.CommandResponse
	writes  int
}

func (script *queueScript) context() context.Context {
	return githubobserver.WithReader(context.Background(), githubobserver.Reader{
		Read: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			script.mu.Lock()
			defer script.mu.Unlock()
			if len(args) < 3 || args[1] != "graphql" || !strings.Contains(args[3], "isInMergeQueue") {
				script.t.Fatalf("unexpected GitHub read %q", args)
			}
			read := script.reads[0]
			if len(script.reads) > 1 {
				script.reads = script.reads[1:]
			}
			if read.fail != nil {
				return nil, read.fail
			}
			return []byte(read.body(script.t)), nil
		},
		Execute: func(_ context.Context, _ string, args ...string) githubobserver.CommandResponse {
			script.mu.Lock()
			defer script.mu.Unlock()
			script.writes++
			want := []string{"api", "graphql", "-f", "query=" + mergeQueueEnqueueMutation, "-f", "id=PR_node", "-f", "head=" + queueHead}
			if !reflect.DeepEqual(args, want) {
				script.t.Fatalf("unexpected GitHub write %q", args)
			}
			return script.enqueue
		},
	})
}

type mergeGroupInspector struct {
	ChecksPolicyInspector
	checks []githubchecks.RemoteCheck
	reason string
	heads  []string
}

func (inspector *mergeGroupInspector) CommitChecks(_ context.Context, options githubchecks.PullRequestWaitOptions) ([]githubchecks.RemoteCheck, bool, string) {
	inspector.heads = append(inspector.heads, options.Head)
	return inspector.checks, false, inspector.reason
}

func queueLandOptions(inspector ChecksPolicyInspector) PullRequestLandOptions {
	return PullRequestLandOptions{Repository: "acme/app", PullRequest: "7", CheckPollInterval: time.Millisecond, Slice: 2 * time.Second, Inspector: inspector}
}

func awaitQueue(t *testing.T, script *queueScript, options PullRequestLandOptions, deadline time.Time) (mergeQueueOutcome, MergeQueueReceipt, error) {
	t.Helper()
	receipt := MergeQueueReceipt{}
	outcome, err := enqueueAndAwaitMergeQueue(script.context(), options, "7", queueHead, "main", deadline, &receipt)
	return outcome, receipt, err
}

func TestMergeQueueEnqueuesThePinnedHeadAndReportsTheMerge(t *testing.T) {
	t.Parallel()
	script := &queueScript{t: t, reads: []queueRead{
		{},
		{inQueue: true, entryState: "AWAITING_CHECKS", position: 1, group: "group-sha"},
		{merged: true, state: "MERGED"},
	}}
	outcome, receipt, err := awaitQueue(t, script, queueLandOptions(nil), time.Now().Add(time.Minute))
	if err != nil || !outcome.merged || outcome.refusal != nil || outcome.pending {
		t.Fatalf("outcome = %+v, err = %v", outcome, err)
	}
	if script.writes != 1 || !receipt.Enqueued || receipt.MergeGroupSHA != "group-sha" || receipt.EntryState != "AWAITING_CHECKS" || receipt.Position != 1 {
		t.Fatalf("writes = %d, receipt = %+v", script.writes, receipt)
	}
}

func TestMergeQueueResumesAnAlreadyQueuedPullRequestWithoutEnqueuingAgain(t *testing.T) {
	t.Parallel()
	script := &queueScript{t: t, reads: []queueRead{
		{inQueue: true, entryState: "QUEUED", position: 2},
		{event: "AddedToMergeQueueEvent"},
		{merged: true, state: "MERGED"},
	}}
	outcome, receipt, err := awaitQueue(t, script, queueLandOptions(nil), time.Now().Add(time.Minute))
	if err != nil || !outcome.merged || script.writes != 0 || receipt.Enqueued {
		t.Fatalf("outcome = %+v, err = %v, writes = %d, receipt = %+v", outcome, err, script.writes, receipt)
	}
}

func TestMergeQueueReportsAFailedMergeGroupWithItsFailingChecks(t *testing.T) {
	t.Parallel()
	script := &queueScript{t: t, reads: []queueRead{
		{},
		{inQueue: true, entryState: "AWAITING_CHECKS", position: 1, group: "group-sha"},
		{inQueue: true, entryState: "UNMERGEABLE", position: 1},
		{event: "RemovedFromMergeQueueEvent", reason: "failed_checks", removedGroup: "rebuilt-group-sha"},
	}}
	inspector := &mergeGroupInspector{checks: []githubchecks.RemoteCheck{{Name: "build", Bucket: "pass"}, {Name: "test", Bucket: "fail"}, {Name: "e2e", Bucket: "cancel"}}}
	outcome, receipt, err := awaitQueue(t, script, queueLandOptions(inspector), time.Now().Add(time.Minute))
	if err != nil || !outcome.findings || outcome.refusal == nil || outcome.refusal.code != LandRefusalMergeGroupFailed {
		t.Fatalf("outcome = %+v, err = %v", outcome, err)
	}
	if !slices.Equal(inspector.heads, []string{"rebuilt-group-sha"}) || !slices.Equal(receipt.FailedChecks, []string{"test", "e2e"}) || receipt.RemovalReason != "failed_checks" {
		t.Fatalf("inspector heads = %v, receipt = %+v", inspector.heads, receipt)
	}
	for _, want := range []string{"removed the pull request without merging it (failed_checks)", "failed test, e2e", "or retry this same head"} {
		if !strings.Contains(outcome.refusal.reason, want) {
			t.Fatalf("reason %q lacks %q", outcome.refusal.reason, want)
		}
	}
	if outcome.refusal.command != "wb pr land acme/app#7 --timeout 45m --requeue" {
		t.Fatalf("command = %q", outcome.refusal.command)
	}
}

func TestMergeQueueReportsAManualDequeueSeparatelyFromAFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, reason, wantCode, wantText string
		inspector                        *mergeGroupInspector
	}{
		{name: "manual removal", reason: "manual", wantCode: LandRefusalMergeQueueDequeued, wantText: "(manual)", inspector: &mergeGroupInspector{reason: "checks unreadable"}},
		{name: "no reason recorded", wantCode: LandRefusalMergeQueueDequeued, wantText: "(no reason recorded)", inspector: &mergeGroupInspector{}},
		{name: "a red optional check does not make a manual removal a failure", reason: "manual", wantCode: LandRefusalMergeQueueDequeued, wantText: "failed lint", inspector: &mergeGroupInspector{checks: []githubchecks.RemoteCheck{{Name: "lint", Bucket: "fail"}}}},
		{name: "timeout", reason: "ci_timeout", wantCode: LandRefusalMergeGroupFailed, wantText: "(ci_timeout)", inspector: &mergeGroupInspector{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			script := &queueScript{t: t, reads: []queueRead{
				{inQueue: true, entryState: "QUEUED", group: "group-sha"},
				{event: "RemovedFromMergeQueueEvent", reason: test.reason},
			}}
			outcome, receipt, err := awaitQueue(t, script, queueLandOptions(test.inspector), time.Now().Add(time.Minute))
			if err != nil || !outcome.findings || outcome.refusal.code != test.wantCode || !strings.Contains(outcome.refusal.reason, test.wantText) || receipt.MergeGroupSHA != "group-sha" {
				t.Fatalf("outcome = %+v, receipt = %+v, err = %v", outcome, receipt, err)
			}
		})
	}
}

func TestMergeQueueWaitsOutAReadThatHasNotYetReportedTheNewEntry(t *testing.T) {
	t.Parallel()
	// Right after enqueuing, GitHub may still report the pull request out of
	// the queue; its newest queue event being the addition proves it was not
	// removed. Only the budget ends that wait, and it ends as pending.
	script := &queueScript{t: t, reads: []queueRead{{}, {event: "AddedToMergeQueueEvent"}}}
	options := queueLandOptions(nil)
	options.CheckPollInterval = 20 * time.Millisecond
	outcome, _, err := awaitQueue(t, script, options, time.Now().Add(100*time.Millisecond))
	if err != nil || !outcome.pending || !strings.Contains(outcome.reason, "GitHub has not yet reported its place in the queue") {
		t.Fatalf("outcome = %+v, err = %v", outcome, err)
	}
}

func TestMergeQueuePendingNamesTheLastObservedPosition(t *testing.T) {
	t.Parallel()
	script := &queueScript{t: t, reads: []queueRead{{inQueue: true, entryState: "QUEUED", position: 3}}}
	options := queueLandOptions(nil)
	options.CheckPollInterval = 20 * time.Millisecond
	outcome, receipt, err := awaitQueue(t, script, options, time.Now().Add(100*time.Millisecond))
	if err != nil || !outcome.pending || !strings.Contains(outcome.reason, "main's merge queue (last seen queued at position 3)") || !strings.Contains(outcome.reason, "GitHub merges it without WB") {
		t.Fatalf("outcome = %+v, receipt = %+v, err = %v", outcome, receipt, err)
	}
}

func TestMergeQueueRefusesBeforeEnqueuingAHeadItDidNotObserve(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		read     queueRead
		wantCode string
		merged   bool
	}{
		{name: "head moved", read: queueRead{head: "2222222222222222222222222222222222222222"}, wantCode: LandRefusalHeadMoved},
		{name: "closed", read: queueRead{state: "CLOSED"}, wantCode: LandRefusalNotOpen},
		{name: "already merged", read: queueRead{state: "MERGED", merged: true}, merged: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			script := &queueScript{t: t, reads: []queueRead{test.read}}
			outcome, _, err := awaitQueue(t, script, queueLandOptions(nil), time.Now().Add(time.Minute))
			if err != nil || script.writes != 0 || outcome.merged != test.merged || test.wantCode != "" && (outcome.refusal == nil || outcome.refusal.code != test.wantCode) {
				t.Fatalf("outcome = %+v, err = %v, writes = %d", outcome, err, script.writes)
			}
		})
	}
}

func TestMergeQueueReportsAHeadPushedWhileQueued(t *testing.T) {
	t.Parallel()
	script := &queueScript{t: t, reads: []queueRead{{inQueue: true}, {head: "3333333333333333333333333333333333333333"}}}
	outcome, _, err := awaitQueue(t, script, queueLandOptions(nil), time.Now().Add(time.Minute))
	if err != nil || outcome.refusal == nil || outcome.refusal.code != LandRefusalHeadMoved || !strings.Contains(outcome.refusal.reason, "takes it out of the merge queue") {
		t.Fatalf("outcome = %+v, err = %v", outcome, err)
	}
}

func TestMergeQueueReportsGitHubRefusingTheEnqueue(t *testing.T) {
	t.Parallel()
	script := &queueScript{t: t, reads: []queueRead{{}}, enqueue: githubobserver.CommandResponse{
		Err: errors.New("exit status 1"), ExitCode: 1, Stderr: []byte("GraphQL: Pull request is not mergeable (enqueuePullRequest)"),
	}}
	outcome, receipt, err := awaitQueue(t, script, queueLandOptions(nil), time.Now().Add(time.Minute))
	if err != nil || outcome.refusal == nil || outcome.refusal.code != LandRefusalMergeQueueEnqueue || receipt.Enqueued ||
		outcome.refusal.reason != "GitHub refused to add the pull request to the merge queue: GraphQL: Pull request is not mergeable (enqueuePullRequest)" {
		t.Fatalf("outcome = %+v, receipt = %+v, err = %v", outcome, receipt, err)
	}
}

func TestMergeQueueRereadsALostEnqueueResponseInsteadOfEnqueuingTwice(t *testing.T) {
	t.Parallel()
	lost := githubobserver.CommandResponse{Err: errors.New("exit status 1"), ExitCode: 1, Stderr: []byte("gh: Bad Gateway (HTTP 502)")}
	for _, test := range []struct {
		name     string
		recheck  queueRead
		adopted  bool
		wantText string
	}{
		{name: "queued after all", recheck: queueRead{inQueue: true}, adopted: true},
		{name: "merged after all", recheck: queueRead{merged: true, state: "MERGED"}, adopted: true},
		{name: "not queued", recheck: queueRead{}, wantText: "enqueue acme/app#7 at 111111111111: gh: Bad Gateway (HTTP 502)"},
		{name: "recheck fails", recheck: queueRead{fail: errors.New("owned reread failure")}, wantText: "gh: Bad Gateway"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			script := &queueScript{t: t, reads: []queueRead{{}, test.recheck, {merged: true, state: "MERGED"}}, enqueue: lost}
			outcome, _, err := awaitQueue(t, script, queueLandOptions(nil), time.Now().Add(time.Minute))
			if script.writes != 1 {
				t.Fatalf("writes = %d, want exactly one enqueue", script.writes)
			}
			if test.adopted {
				if err != nil || !outcome.merged {
					t.Fatalf("outcome = %+v, err = %v", outcome, err)
				}
				return
			}
			if !errors.Is(err, githubobserver.ErrTransientMutationOutcomeUnknown) || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestMergeQueueSurfacesUnreadableState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		reads []queueRead
		want  string
	}{
		{name: "first read", reads: []queueRead{{fail: errors.New("HTTP 401")}}, want: "read merge queue state of acme/app#7: HTTP 401"},
		{name: "observation read", reads: []queueRead{{inQueue: true}, {fail: errors.New("HTTP 401")}}, want: "HTTP 401"},
		{name: "malformed", reads: []queueRead{{raw: "{"}}, want: "decode merge queue state of acme/app#7"},
		{name: "no pull request", reads: []queueRead{{raw: `{"data":{"repository":{"pullRequest":null}}}`}}, want: "carried no pull request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			script := &queueScript{t: t, reads: test.reads}
			_, _, err := awaitQueue(t, script, queueLandOptions(nil), time.Now().Add(time.Minute))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMergeQueueStopsWaitingWhenTheCallerCancels(t *testing.T) {
	t.Parallel()
	script := &queueScript{t: t, reads: []queueRead{{inQueue: true}}}
	ctx, cancel := context.WithCancel(script.context())
	cancel()
	options := queueLandOptions(nil)
	options.CheckPollInterval = 0 // the default interval, which cancellation must cut short
	_, err := enqueueAndAwaitMergeQueue(ctx, options, "7", queueHead, "main", time.Now().Add(time.Hour), &MergeQueueReceipt{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeGroupFailedChecksNeedsAMergeGroup(t *testing.T) {
	t.Parallel()
	inspector := &mergeGroupInspector{}
	if failed := mergeGroupFailedChecks(context.Background(), queueLandOptions(inspector), "main", ""); failed != nil || len(inspector.heads) != 0 {
		t.Fatalf("failed = %v, reads = %v", failed, inspector.heads)
	}
}

func TestMergeQueueRefusesFlagsTheQueueWouldDiscard(t *testing.T) {
	t.Parallel()
	policy := githubchecks.MergeQueuePolicy{RulesetID: 9, MergeMethod: "merge"}
	base := PullRequestLandOptions{Repository: "acme/app", Keep: true}
	for _, test := range []struct {
		name   string
		change func(*PullRequestLandOptions)
		want   string
	}{
		{name: "keep commits", change: func(o *PullRequestLandOptions) {
			o.KeepCommits, o.Reason, o.MergeMethod, o.MergeMethodExplicit = []string{"abc"}, "why", "squash", true
		}, want: "--keep-commits rebuilds the branch"},
		{name: "subject", change: func(o *PullRequestLandOptions) {
			o.Subject, o.MergeMethod, o.MergeMethodExplicit = "title", "squash", true
		}, want: "--subject writes a squash commit message"},
		{name: "merge method", change: func(o *PullRequestLandOptions) { o.MergeMethod, o.MergeMethodExplicit = "rebase", true }, want: "--merge-method rebase differs from the queue's own method"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := base
			test.change(&options)
			refusal := mergeQueueOptionConflict(options, policy, "main", "7")
			if refusal == nil || refusal.code != LandRefusalMergeQueueOption || !strings.Contains(refusal.reason, test.want) ||
				!strings.Contains(refusal.reason, "main requires merge queue ruleset 9, which merges the pull request head itself with its own method (merge)") {
				t.Fatalf("refusal = %+v", refusal)
			}
			if refusal.command != "wb pr land acme/app#7 --keep" {
				t.Fatalf("command = %q, want the same landing without the discarded flags", refusal.command)
			}
		})
	}
	for _, options := range []PullRequestLandOptions{base, {MergeMethod: "merge", MergeMethodExplicit: true}, {MergeMethod: "squash"}} {
		if refusal := mergeQueueOptionConflict(options, policy, "main", "7"); refusal != nil {
			t.Fatalf("options %+v refused: %+v", options, refusal)
		}
	}
}

func TestLandThroughMergeQueueReturnsAnUnreadableQueueAsAnError(t *testing.T) {
	t.Parallel()
	script := &queueScript{t: t, reads: []queueRead{{fail: errors.New("HTTP 401")}}}
	result := PullRequestLandResult{ManualEquivalent: make([]string, 8), Evidence: map[string]string{}, MergeQueue: &MergeQueueReceipt{}}
	view := githubchecks.PullRequestView{}
	view.Head.SHA, view.Base.Ref = queueHead, "main"
	_, err := landThroughMergeQueue(script.context(), queueLandOptions(nil), result, view, "7", time.Now().Add(time.Minute))
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || script.writes != 0 {
		t.Fatalf("err = %v, writes = %d", err, script.writes)
	}
}

func TestMergeQueueWaitsForTheMergeWhenGitHubRecordsTheRemovalFirst(t *testing.T) {
	t.Parallel()
	// GitHub records the queue's own merge as a removal with reason "merged"
	// about a second before the pull request reports itself merged.
	mergedRemoval := queueRead{event: "RemovedFromMergeQueueEvent", reason: "merged", removedAt: "2026-10-15T07:01:16Z"}
	for _, test := range []struct {
		name  string
		reads []queueRead
	}{
		{name: "seen while observing", reads: []queueRead{{inQueue: true}, mergedRemoval, {merged: true, state: "MERGED"}}},
		{name: "seen on the first read", reads: []queueRead{mergedRemoval, {merged: true, state: "MERGED"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			script := &queueScript{t: t, reads: test.reads}
			outcome, _, err := awaitQueue(t, script, queueLandOptions(nil), time.Now().Add(time.Minute))
			if err != nil || !outcome.merged || script.writes != 0 {
				t.Fatalf("outcome = %+v, err = %v, writes = %d", outcome, err, script.writes)
			}
		})
	}
}

func TestMergeQueueReportsARemovalNobodyWatchedInsteadOfEnqueuingTheSameHeadAgain(t *testing.T) {
	t.Parallel()
	removed := queueRead{event: "RemovedFromMergeQueueEvent", reason: "failed_checks", removedAt: "2026-10-15T13:41:14Z", removedGroup: "group-sha", committedAt: "2026-10-15T13:00:00Z"}
	inspector := &mergeGroupInspector{checks: []githubchecks.RemoteCheck{{Name: "test", Bucket: "fail"}}}
	script := &queueScript{t: t, reads: []queueRead{removed}}
	outcome, receipt, err := awaitQueue(t, script, queueLandOptions(inspector), time.Now().Add(time.Minute))
	if err != nil || script.writes != 0 || !outcome.findings || outcome.refusal.code != LandRefusalMergeGroupFailed ||
		outcome.refusal.command != "wb pr land acme/app#7 --timeout 45m --requeue" || !slices.Equal(receipt.FailedChecks, []string{"test"}) {
		t.Fatalf("outcome = %+v, receipt = %+v, err = %v, writes = %d", outcome, receipt, err, script.writes)
	}
}

func TestMergeQueueEnqueuesARemovedHeadAgainOnlyWhenAskedOrPushedSince(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, reason, committedAt, suiteAt string
		requeue                            bool
	}{
		{name: "explicit requeue", reason: "failed_checks", committedAt: "2026-10-15T13:00:00Z", requeue: true},
		{name: "new head committed after the removal", reason: "failed_checks", committedAt: "2026-10-15T14:00:00Z"},
		// A fix committed before the removal but pushed after it: the check
		// suite GitHub created on push, not the commit date, tells it apart.
		{name: "new head pushed after the removal", reason: "failed_checks", committedAt: "2026-10-15T13:00:00Z", suiteAt: "2026-10-15T13:45:00Z"},
		{name: "a manual removal is retried without asking", reason: "manual", committedAt: "2026-10-15T13:00:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			removed := queueRead{event: "RemovedFromMergeQueueEvent", reason: test.reason, removedAt: "2026-10-15T13:41:14Z", committedAt: test.committedAt, suiteAt: test.suiteAt}
			script := &queueScript{t: t, reads: []queueRead{removed, {merged: true, state: "MERGED"}}}
			options := queueLandOptions(nil)
			options.Requeue = test.requeue
			outcome, receipt, err := awaitQueue(t, script, options, time.Now().Add(time.Minute))
			if err != nil || !outcome.merged || script.writes != 1 || !receipt.Enqueued {
				t.Fatalf("outcome = %+v, err = %v, writes = %d", outcome, err, script.writes)
			}
		})
	}
}

func TestMergeQueueRecordsTheEntryTheEnqueueReturned(t *testing.T) {
	t.Parallel()
	script := &queueScript{t: t, reads: []queueRead{{}, {event: "AddedToMergeQueueEvent"}}, enqueue: githubobserver.CommandResponse{
		Stdout: []byte(`{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"state":"QUEUED","position":4}}}}`),
	}}
	options := queueLandOptions(nil)
	options.CheckPollInterval = 20 * time.Millisecond
	outcome, receipt, err := awaitQueue(t, script, options, time.Now().Add(100*time.Millisecond))
	if err != nil || !outcome.pending || receipt.Position != 4 || !strings.Contains(outcome.reason, "last seen queued at position 4") {
		t.Fatalf("outcome = %+v, receipt = %+v, err = %v", outcome, receipt, err)
	}
}

func TestMergeQueueReleasesTheLandingLaneOnceQueued(t *testing.T) {
	t.Parallel()
	script := &queueScript{t: t, reads: []queueRead{{}, {merged: true, state: "MERGED"}}}
	releases := 0
	options := queueLandOptions(nil)
	options.releaseLane = func() {
		if script.writes != 1 {
			t.Errorf("lane released before the pull request was queued")
		}
		releases++
	}
	outcome, _, err := awaitQueue(t, script, options, time.Now().Add(time.Minute))
	if err != nil || !outcome.merged || releases != 1 {
		t.Fatalf("outcome = %+v, err = %v, releases = %d", outcome, err, releases)
	}
}

func TestMergeQueueRemovalKindUsesGitHubsTokens(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]string{
		"merged": mergeQueueRemovedMerging, "already_merged": mergeQueueRemovedMerging,
		"failed_checks": mergeQueueRemovedFailed, "ci_timeout": mergeQueueRemovedFailed, "timed_out": mergeQueueRemovedFailed,
		"manual": mergeQueueRemovedOther, "": mergeQueueRemovedOther, "merge_conflict": mergeQueueRemovedOther,
	} {
		if got := mergeQueueRemovalKind(reason); got != want {
			t.Errorf("mergeQueueRemovalKind(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestMergeQueueIgnoresTheEarlierRemovalAStaleReadStillShowsAfterEnqueuing(t *testing.T) {
	t.Parallel()
	earlier := queueRead{event: "RemovedFromMergeQueueEvent", reason: "failed_checks", removedAt: "2026-10-15T13:41:14Z", committedAt: "2026-10-15T13:00:00Z"}
	script := &queueScript{t: t, reads: []queueRead{earlier, earlier, {inQueue: true}, {merged: true, state: "MERGED"}}}
	options := queueLandOptions(nil)
	options.Requeue = true
	outcome, _, err := awaitQueue(t, script, options, time.Now().Add(time.Minute))
	if err != nil || !outcome.merged || script.writes != 1 {
		t.Fatalf("outcome = %+v, err = %v, writes = %d", outcome, err, script.writes)
	}
}

func TestMergeQueueReportsANewRemovalBeforeTheEntryWasEverSeen(t *testing.T) {
	t.Parallel()
	fast := queueRead{event: "RemovedFromMergeQueueEvent", reason: "failed_checks", removedAt: "2026-10-15T13:42:00Z"}
	script := &queueScript{t: t, reads: []queueRead{{}, fast}}
	outcome, _, err := awaitQueue(t, script, queueLandOptions(&mergeGroupInspector{}), time.Now().Add(time.Minute))
	if err != nil || !outcome.findings || outcome.refusal.code != LandRefusalMergeGroupFailed {
		t.Fatalf("outcome = %+v, err = %v", outcome, err)
	}
}

func TestMergeQueueSpendsRequeueOnceEnqueued(t *testing.T) {
	t.Parallel()
	script := &queueScript{t: t, reads: []queueRead{{}, {inQueue: true}}}
	options := queueLandOptions(nil)
	options.Requeue, options.CheckPollInterval = true, 20*time.Millisecond
	result := PullRequestLandResult{ManualEquivalent: make([]string, 8), Evidence: map[string]string{}, MergeQueue: &MergeQueueReceipt{}}
	view := githubchecks.PullRequestView{}
	view.Head.SHA, view.Base.Ref = queueHead, "main"
	landed, err := landThroughMergeQueue(script.context(), options, result, view, "7", time.Now().Add(100*time.Millisecond))
	if err != nil || landed.RefusalCode != LandRefusalMergeQueuePending || strings.Contains(landed.SanctionedCommand, "--requeue") {
		t.Fatalf("result = %+v, err = %v", landed, err)
	}
	guided := withPullRequestLandResumeGuidance(fmt.Errorf("%w: lost", githubobserver.ErrTransientMutationOutcomeUnknown), options, landed)
	if strings.Contains(guided.Error(), "--requeue") || !strings.Contains(guided.Error(), "resumable: wb pr land acme/app#7") {
		t.Fatalf("resume guidance = %v", guided)
	}
}
