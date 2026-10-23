package worktrees

import (
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreelanding"
)

const (
	refusalHead   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	refusalTarget = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	refusalRemote = "cccccccccccccccccccccccccccccccccccccccc"
)

func cleanCandidate() ListResult {
	return ListResult{
		Task: "fixture-task", Repository: "acme/app", Branch: "fixture-task", Base: "main",
		HeadSHA: refusalHead, RemoteTargetSHA: refusalTarget, Clean: true,
	}
}

// Every refusal that compares a head against a target names the exact ref and
// SHA it used. "(awaiting push)" alone was wrong for a branch that is fully
// pushed and merely not merged into the target it was judged against.
func TestCleanupRefusalNamesTheTargetRefAndSHAItComparedAgainst(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*ListResult)
		want   string
	}{
		{
			name:   "pushed and unmerged",
			mutate: func(entry *ListResult) { entry.RemoteHeadSHA = refusalHead },
			want:   "current branch head bbbbbbbbbbbb is not integrated into the exact origin target origin/main at aaaaaaaaaaaa (pushed to origin/fixture-task, awaiting merge)",
		},
		{
			name:   "source branch not on origin",
			mutate: func(*ListResult) {},
			want:   "current branch head bbbbbbbbbbbb is not integrated into the exact origin target origin/main at aaaaaaaaaaaa (awaiting push)",
		},
		{
			name: "recorded base absent and no receipt",
			mutate: func(entry *ListResult) {
				entry.RemoteHeadSHA = refusalHead
				entry.RecordedBase, entry.RecordedBaseState = "integration", worktreelanding.RecordedBaseAbsent
				entry.TargetRejection = "recorded target origin/integration is absent and GitHub has no exact merged receipt"
			},
			want: "current branch head bbbbbbbbbbbb is not integrated into the exact origin target origin/main at aaaaaaaaaaaa (pushed to origin/fixture-task, awaiting merge)" +
				"; recorded base integration is absent: recorded target origin/integration is absent and GitHub has no exact merged receipt",
		},
		{
			name: "explicit base differs from the recorded one",
			mutate: func(entry *ListResult) {
				entry.RecordedBase = "integration"
				entry.AbsorbedByRejection = "pull request #7 merged into \"release\", not the requested base \"main\""
			},
			want: "current branch head bbbbbbbbbbbb is not integrated into the exact origin target origin/main at aaaaaaaaaaaa (awaiting push)" +
				"; recorded base integration: pull request #7 merged into \"release\", not the requested base \"main\"",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			entry := cleanCandidate()
			test.mutate(&entry)
			eligible, reason := cleanupSafetyEligibility(entry, 0, now, false)
			if eligible || reason != test.want {
				t.Fatalf("eligible = %t, reason = %q; want a refusal reading %q", eligible, reason, test.want)
			}
		})
	}
}

func TestCleanupRefusalNamesBothHeadsWhenTheRemoteBranchAdvanced(t *testing.T) {
	t.Parallel()
	entry := cleanCandidate()
	entry.IntegratedAtOrigin, entry.RemoteHeadSHA = true, refusalRemote
	eligible, reason := cleanupSafetyEligibility(entry, 0, time.Now(), false)
	want := "remote branch advanced after the merged pull request: origin/fixture-task is at cccccccccccc, not the local head bbbbbbbbbbbb"
	if eligible || reason != want {
		t.Fatalf("eligible = %t, reason = %q; want %q", eligible, reason, want)
	}
}

// A task created by a subagent records the process id of the session harness
// above it, which outlives the subagent by hours. owner_state then reads
// "active" for as long as the coordinating session lives. That is a fact about
// a process, and cleanup decides on the worktree: an integrated, clean, pushed
// head is eligible whoever is still alive.
func TestCleanupDoesNotRefuseAnIntegratedTaskBecauseItsOwnerProcessIsAlive(t *testing.T) {
	t.Parallel()
	entry := cleanCandidate()
	entry.IntegratedAtOrigin, entry.RemoteHeadSHA = true, refusalHead
	entry.OwnerState = "active"
	entry.Owners = []OwnerView{{OwnerRegistration: OwnerRegistration{PID: 22613}, PIDStatus: "active"}}
	if eligible, reason := cleanupSafetyEligibility(entry, 0, time.Now(), false); !eligible {
		t.Fatalf("a live owner process blocked an integrated task: %s", reason)
	}
	// The same candidate, unintegrated, is refused for that and nothing else.
	entry.IntegratedAtOrigin = false
	if eligible, reason := cleanupSafetyEligibility(entry, 0, time.Now(), false); eligible || strings.Contains(reason, "owner") {
		t.Fatalf("eligible = %t, reason = %q", eligible, reason)
	}
}
