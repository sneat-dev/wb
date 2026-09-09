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
	at := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	snapshot := FromRemoteSnapshot(remotestate.Snapshot{
		Login: "alice", Machine: "laptop", PublishedAt: at,
		ProjectsRoot: "/Users/alice/private", WBVersion: "secret-build",
		KnownRepositories: []string{"zeta/tools", "acme/widgets", "acme/widgets"},
		Repositories: []remotestate.RepositoryState{{
			Repository: "acme/widgets", Path: "/Users/alice/private/acme/widgets",
			Summary: "private command output", Unpushed: []string{"private commit subject"},
		}},
		Worktrees: []remotestate.WorktreeState{{
			Task: "dashboard", Stream: "fleet", Repository: "acme/widgets", Branch: "feature/dashboard",
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
	if snapshot.Worktrees[0].AttentionReason != machinesnapshot.AttentionReviewRequired {
		t.Fatalf("attention reason = %q", snapshot.Worktrees[0].AttentionReason)
	}
	receivedAt := at.Add(time.Second)
	converted := Entry(machinesnapshot.StoredSnapshot{Snapshot: snapshot, ReceivedAt: receivedAt})
	if converted.Snapshot.ProjectsRoot != "" || converted.Snapshot.Worktrees[0].Dir != "" || converted.Snapshot.Worktrees[0].HeadSHA != "" {
		t.Fatalf("read-model adapter restored local data: %+v", converted.Snapshot)
	}
	if got := strings.Join(converted.Snapshot.KnownRepositories, ","); got != "acme/widgets,zeta/tools" {
		t.Fatalf("known repositories = %q", got)
	}
	if !converted.Snapshot.Heartbeat().Equal(receivedAt) {
		t.Fatalf("heartbeat = %s, want server receipt %s", converted.Snapshot.Heartbeat(), receivedAt)
	}
}
