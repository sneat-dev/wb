package hub

import (
	"sort"
	"strings"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// FromRemoteSnapshot removes every field outside the public hosted allowlist.
func FromRemoteSnapshot(source remotestate.Snapshot) machinesnapshot.Snapshot {
	result := machinesnapshot.Snapshot{
		SchemaVersion: machinesnapshot.SchemaVersion,
		Login:         source.Login, Machine: source.Machine,
		PublishedAt: source.PublishedAt, LastSeenAt: source.LastSeenAt,
		Repositories: hostedRepositories(source.KnownRepositories),
		Worktrees:    make([]machinesnapshot.Worktree, 0, len(source.Worktrees)),
	}
	for _, sourceWorktree := range source.Worktrees {
		result.Worktrees = append(result.Worktrees, machinesnapshot.Worktree{
			Task: sourceWorktree.Task, Stream: sourceWorktree.Stream,
			Repository: sourceWorktree.Repository, Branch: sourceWorktree.Branch,
			Lifecycle: sourceWorktree.Lifecycle, OwnerState: sourceWorktree.OwnerState,
			Owner: sourceWorktree.Owner, LastActivityAt: sourceWorktree.LastActivityAt,
			NeedsAttention:  sourceWorktree.NeedsAttention,
			AttentionReason: machinesnapshot.NormalizeAttentionReason(sourceWorktree.NeedsAttention, sourceWorktree.Attention),
			PullRequest:     hostedPullRequest(sourceWorktree.PullRequest),
		})
	}
	return result
}

// Entry converts a stored hosted record to the existing read-model input while
// leaving every local-only field at its zero value. Server receipt time is the
// authoritative hosted heartbeat used for stale-machine labeling.
func Entry(stored machinesnapshot.StoredSnapshot) remotestate.Entry {
	lastSeenAt := stored.Snapshot.LastSeenAt
	if stored.ReceivedAt.After(lastSeenAt) {
		lastSeenAt = stored.ReceivedAt
	}
	snapshot := remotestate.Snapshot{
		SchemaVersion: remotestate.SchemaVersion,
		Login:         stored.Snapshot.Login, Machine: stored.Snapshot.Machine,
		PublishedAt: stored.Snapshot.PublishedAt, LastSeenAt: lastSeenAt,
		KnownRepositories: remoteRepositories(stored.Snapshot.Repositories),
		Worktrees:         make([]remotestate.WorktreeState, 0, len(stored.Snapshot.Worktrees)),
	}
	for _, worktree := range stored.Snapshot.Worktrees {
		snapshot.Worktrees = append(snapshot.Worktrees, remotestate.WorktreeState{
			Task: worktree.Task, Stream: worktree.Stream, Repository: worktree.Repository,
			Branch: worktree.Branch, Lifecycle: worktree.Lifecycle,
			OwnerState: worktree.OwnerState, Owner: worktree.Owner,
			LastActivityAt: worktree.LastActivityAt, NeedsAttention: worktree.NeedsAttention,
			Attention: worktree.AttentionReason, PullRequest: remotePullRequest(worktree.PullRequest),
		})
	}
	return remotestate.Entry{Snapshot: snapshot}
}

func hostedRepositories(source []string) []string {
	seen := make(map[string]bool, len(source))
	result := make([]string, 0, len(source))
	for _, repository := range source {
		canonical := repository
		if !strings.HasPrefix(canonical, "github.com/") {
			canonical = "github.com/" + canonical
		}
		if !seen[canonical] {
			seen[canonical] = true
			result = append(result, canonical)
		}
	}
	sort.Strings(result)
	return result
}

func remoteRepositories(source []string) []string {
	result := make([]string, len(source))
	for index, repository := range source {
		result[index] = strings.TrimPrefix(repository, "github.com/")
	}
	return result
}

func hostedPullRequest(value *remotestate.PullRequestState) *machinesnapshot.PullRequest {
	if value == nil {
		return nil
	}
	return &machinesnapshot.PullRequest{Number: value.Number, URL: value.URL, State: value.State}
}

func remotePullRequest(value *machinesnapshot.PullRequest) *remotestate.PullRequestState {
	if value == nil {
		return nil
	}
	return &remotestate.PullRequestState{Number: value.Number, URL: value.URL, State: value.State}
}
