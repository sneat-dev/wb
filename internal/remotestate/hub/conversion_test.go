package hub

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/internal/remotestate"
)

func TestFromRemoteSnapshotIsStrictPrivacyAllowlist(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	snapshot := FromRemoteSnapshot(remotestate.Snapshot{
		Login: "alice", Machine: "laptop", PublishedAt: at, RemoteStore: "git:team/wb-state",
		ProjectsRoot: "/Users/alice/private", WBVersion: "secret-build",
		KnownRepositories: []string{"zeta/tools", "acme/widgets", "acme/widgets"},
		Repositories: []remotestate.RepositoryState{{
			Repository: "acme/widgets", Path: "/Users/alice/private/acme/widgets",
			Summary: "private command output", Unpushed: []string{"private commit subject"},
		}},
		Worktrees: []remotestate.WorktreeState{{
			Task: "dashboard", TaskSummary: "Repair worktree activity", Stream: "fleet", Repository: "acme/widgets", Branch: "feature/dashboard",
			Dir: "/Users/alice/private/.worktrees/dashboard", HeadSHA: "0123456789abcdef",
			Lifecycle: "review", OwnerState: "active", Owner: "worker-1",
			NeedsAttention: true, Attention: "/Users/alice/private command output",
			PullRequest: &remotestate.PullRequestState{Number: 7, URL: "https://github.com/acme/widgets/pull/7", State: "open"},
		}},
	})
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"/Users/alice", "projects_root", "private command output", "private commit subject", "head_sha", "0123456789abcdef", "wb_version"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("hosted snapshot contains forbidden %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"repository":"acme/widgets"`) || !strings.Contains(text, `"number":7`) {
		t.Fatalf("hosted snapshot lost dashboard fields: %s", text)
	}
	if !strings.Contains(text, `"repositories":["github.com/acme/widgets","github.com/zeta/tools"]`) {
		t.Fatalf("hosted snapshot lost sorted canonical repository enrollment: %s", text)
	}
	if !strings.Contains(text, `"remote_store":"git:team/wb-state"`) {
		t.Fatalf("hosted snapshot lost remote-store provenance: %s", text)
	}
	if snapshot.Worktrees[0].AttentionReason != machinesnapshot.AttentionReviewRequired {
		t.Fatalf("attention reason = %q", snapshot.Worktrees[0].AttentionReason)
	}
	if snapshot.Worktrees[0].TaskSummary != "Repair worktree activity" {
		t.Fatalf("hosted task summary = %q", snapshot.Worktrees[0].TaskSummary)
	}
	var wireSnapshot machinesnapshot.Snapshot
	if err := json.Unmarshal(raw, &wireSnapshot); err != nil {
		t.Fatal(err)
	}
	receivedAt := at.Add(time.Second)
	converted := Entry(machinesnapshot.StoredSnapshot{Snapshot: wireSnapshot, ReceivedAt: receivedAt})
	if converted.Snapshot.ProjectsRoot != "" || converted.Snapshot.Worktrees[0].Dir != "" || converted.Snapshot.Worktrees[0].HeadSHA != "" {
		t.Fatalf("read-model adapter restored local data: %+v", converted.Snapshot)
	}
	if got := strings.Join(converted.Snapshot.KnownRepositories, ","); got != "acme/widgets,zeta/tools" {
		t.Fatalf("known repositories = %q", got)
	}
	if !converted.Snapshot.Heartbeat().Equal(receivedAt) {
		t.Fatalf("heartbeat = %s, want server receipt %s", converted.Snapshot.Heartbeat(), receivedAt)
	}
	if converted.Snapshot.Worktrees[0].TaskSummary != "Repair worktree activity" {
		t.Fatalf("read-model task summary = %q", converted.Snapshot.Worktrees[0].TaskSummary)
	}
	if converted.Snapshot.RemoteStore != "git:team/wb-state" {
		t.Fatalf("read-model remote store = %q", converted.Snapshot.RemoteStore)
	}
}

func TestOptionalFieldsCrossTheHostedBoundaryBothWays(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	cpu, used, total := 40.0, uint64(1), uint64(2)
	source := remotestate.Snapshot{
		Login: "alice", Machine: "laptop", PublishedAt: at, ProjectsRoot: "/Users/alice/private",
		OS: "linux", Arch: "arm64", CPUCount: 8, BootTime: at.Add(-time.Hour),
		Agents:  []remotestate.AgentState{{Kind: "session", SessionID: "wbs-1", Runtime: "claude", Model: "opus", State: "live", Activity: "working", Task: "fix", Repository: "acme/widgets", StartedAt: at}},
		Metrics: &remotestate.MetricsSample{CPUPercent: &cpu, MemoryUsedBytes: &used, MemoryTotalBytes: &total, SampledAt: at},
	}
	hosted := FromRemoteSnapshot(source)
	if err := hosted.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(hosted)
	for _, want := range []string{`"os":"linux"`, `"cpu_count":8`, `"session_id":"wbs-1"`, `"cpu_percent":40`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("hosted snapshot lacks %s: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), "/Users/alice") {
		t.Errorf("hosted snapshot carries a path: %s", raw)
	}
	back := Entry(machinesnapshot.StoredSnapshot{Snapshot: hosted, ReceivedAt: at}).Snapshot
	if back.OS != "linux" || back.Arch != "arm64" || back.CPUCount != 8 || !back.BootTime.Equal(source.BootTime) ||
		len(back.Agents) != 1 || back.Agents[0] != source.Agents[0] || back.Metrics == nil || *back.Metrics.CPUPercent != 40 {
		t.Fatalf("read-model entry = %+v", back)
	}
	bare := FromRemoteSnapshot(remotestate.Snapshot{Login: "alice", Machine: "laptop", PublishedAt: at})
	if bare.Agents != nil || bare.Metrics != nil || Entry(machinesnapshot.StoredSnapshot{Snapshot: bare, ReceivedAt: at}).Snapshot.Metrics != nil {
		t.Errorf("a snapshot without optional fields gained some: %+v", bare)
	}
}
