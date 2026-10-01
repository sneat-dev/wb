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
		RemoteStore:  source.RemoteStore,
		Repositories: hostedRepositories(source.KnownRepositories),
		Worktrees:    make([]machinesnapshot.Worktree, 0, len(source.Worktrees)),
		OS:           source.OS, Arch: source.Arch, CPUCount: source.CPUCount, BootTime: source.BootTime,
		Agents: hostedAgents(source.Agents), Metrics: hostedMetrics(source.Metrics),
	}
	for _, sourceWorktree := range source.Worktrees {
		result.Worktrees = append(result.Worktrees, machinesnapshot.Worktree{
			Task: sourceWorktree.Task, TaskSummary: sourceWorktree.TaskSummary, Stream: sourceWorktree.Stream,
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
		RemoteStore:       stored.Snapshot.RemoteStore,
		KnownRepositories: remoteRepositories(stored.Snapshot.Repositories),
		Worktrees:         make([]remotestate.WorktreeState, 0, len(stored.Snapshot.Worktrees)),
		OS:                stored.Snapshot.OS, Arch: stored.Snapshot.Arch, CPUCount: stored.Snapshot.CPUCount, BootTime: stored.Snapshot.BootTime,
		Agents: remoteAgents(stored.Snapshot.Agents), Metrics: remoteMetrics(stored.Snapshot.Metrics),
	}
	for _, worktree := range stored.Snapshot.Worktrees {
		snapshot.Worktrees = append(snapshot.Worktrees, remotestate.WorktreeState{
			Task: worktree.Task, TaskSummary: worktree.TaskSummary, Stream: worktree.Stream, Repository: worktree.Repository,
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
		canonical := strings.ToLower(repository)
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
		result[index] = strings.TrimPrefix(strings.ToLower(repository), "github.com/")
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

// hostedAgents copies the closed agent fields; the copy is nil for none so the
// hosted document omits the list.
func hostedAgents(source []remotestate.AgentState) []machinesnapshot.Agent {
	if len(source) == 0 {
		return nil
	}
	result := make([]machinesnapshot.Agent, len(source))
	for index, agent := range source {
		result[index] = machinesnapshot.Agent{
			Kind: agent.Kind, SessionID: agent.SessionID, RunID: agent.RunID, Runtime: agent.Runtime, Model: agent.Model,
			State: agent.State, Activity: agent.Activity, Task: agent.Task, Repository: agent.Repository, StartedAt: agent.StartedAt,
		}
	}
	return result
}

func remoteAgents(source []machinesnapshot.Agent) []remotestate.AgentState {
	if len(source) == 0 {
		return nil
	}
	result := make([]remotestate.AgentState, len(source))
	for index, agent := range source {
		result[index] = remotestate.AgentState{
			Kind: agent.Kind, SessionID: agent.SessionID, RunID: agent.RunID, Runtime: agent.Runtime, Model: agent.Model,
			State: agent.State, Activity: agent.Activity, Task: agent.Task, Repository: agent.Repository, StartedAt: agent.StartedAt,
		}
	}
	return result
}

func hostedMetrics(source *remotestate.MetricsSample) *machinesnapshot.Metrics {
	if source == nil {
		return nil
	}
	return &machinesnapshot.Metrics{
		CPUPercent: source.CPUPercent, Load1: source.Load1, MemoryUsedBytes: source.MemoryUsedBytes, MemoryTotalBytes: source.MemoryTotalBytes,
		DiskFreeBytes: source.DiskFreeBytes, DiskTotalBytes: source.DiskTotalBytes, SampledAt: source.SampledAt,
	}
}

func remoteMetrics(source *machinesnapshot.Metrics) *remotestate.MetricsSample {
	if source == nil {
		return nil
	}
	return &remotestate.MetricsSample{
		CPUPercent: source.CPUPercent, Load1: source.Load1, MemoryUsedBytes: source.MemoryUsedBytes, MemoryTotalBytes: source.MemoryTotalBytes,
		DiskFreeBytes: source.DiskFreeBytes, DiskTotalBytes: source.DiskTotalBytes, SampledAt: source.SampledAt,
	}
}
