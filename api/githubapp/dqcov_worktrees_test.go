package githubapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

func TestDQCovRemoteStateWorktreeReadModelPropagatesStoreFailure(t *testing.T) {
	store := &readSnapshotStore{err: errors.New("snapshots unavailable")}
	model := RemoteStateWorktreeReadModel{Store: store, Access: machineAccess{}}
	_, err := model.Worktrees(context.Background(), Viewer{Authenticated: true, Member: true, UserID: "user"}, WorktreeFilter{})
	if err == nil || err.Error() != "snapshots unavailable" {
		t.Fatalf("err = %v, store calls = %d", err, store.calls)
	}
}

func TestDQCovRemoteStateWorktreeReadModelSkipsUnvalidatedRecords(t *testing.T) {
	invalid := machinesnapshot.StoredSnapshot{
		Snapshot:   machinesnapshot.Snapshot{Login: "alex", Machine: "vm"},
		ReceivedAt: time.Unix(1, 0), Digest: "digest",
	}
	missingReceipt := storedMachine("alex", "vm", time.Unix(2, 0), time.Time{}, nil)
	model := RemoteStateWorktreeReadModel{
		Store:  &readSnapshotStore{records: []machinesnapshot.StoredSnapshot{invalid, missingReceipt}},
		Access: machineAccess{allowed: map[string]bool{"alex/vm": true}},
	}
	access, err := model.Worktrees(context.Background(), Viewer{Authenticated: true, Member: true, UserID: "user"}, WorktreeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(access.Value.Rows) != 0 {
		t.Fatalf("rows = %+v, want none for unvalidated records", access.Value.Rows)
	}
}

func TestDQCovRemoteStateWorktreeReadModelSortsRowsDeterministically(t *testing.T) {
	published := time.Unix(1000, 0).UTC()
	store := &readSnapshotStore{records: []machinesnapshot.StoredSnapshot{
		storedMachine("alex", "beta", published, published, []machinesnapshot.Worktree{
			{Repository: "acme/app", Task: "zeta", Branch: "b"},
		}),
		storedMachine("alex", "alpha", published, published, []machinesnapshot.Worktree{
			{Repository: "acme/app", Task: "zeta", Branch: "b"},
			{Repository: "acme/app", Task: "alpha", Branch: "b"},
		}),
	}}
	model := RemoteStateWorktreeReadModel{
		Store:  store,
		Access: machineAccess{allowed: map[string]bool{"alex/alpha": true, "alex/beta": true}},
	}
	access, err := model.Worktrees(context.Background(), Viewer{Authenticated: true, Member: true, UserID: "user"}, WorktreeFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(access.Value.Rows))
	for _, row := range access.Value.Rows {
		got = append(got, row.Machine+"/"+row.Task)
	}
	want := []string{"alpha/alpha", "alpha/zeta", "beta/zeta"}
	if len(got) != len(want) {
		t.Fatalf("rows = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("rows = %#v, want %#v", got, want)
		}
	}
}

func TestDQCovWorktreeRowDefaultsAndAttentionReasons(t *testing.T) {
	published := time.Unix(1000, 0).UTC()
	received := published.Add(time.Minute)
	snapshot := machinesnapshot.Snapshot{Machine: "vm", PublishedAt: published}

	row := worktreeRow(snapshot, received, machinesnapshot.Worktree{
		Repository: "github.com/acme/app", Task: "task", Branch: "branch",
	}, received, time.Hour)
	if row.Lifecycle != "working" || row.OwnerStatus != "unknown" || row.Status != "working" || row.NeedsAttention {
		t.Fatalf("defaulted row = %+v", row)
	}
	if !row.MachineSeenAt.Equal(received) || row.MachineStale {
		t.Fatalf("fresh receipt row = %+v", row)
	}

	whitelisted := "supersession evidence requires review"
	attention := worktreeRow(snapshot, received, machinesnapshot.Worktree{
		Repository: "github.com/acme/app", Task: "task", Branch: "branch", AttentionReason: "  " + whitelisted + "  ",
	}, received, time.Hour)
	if !attention.NeedsAttention || attention.AttentionReason != whitelisted || attention.Status != "attention" {
		t.Fatalf("whitelisted attention = %+v", attention)
	}
	if attention.Lifecycle != "working" || attention.OwnerStatus != "unknown" {
		t.Fatalf("attention row defaults = %+v", attention)
	}

	sanitized := worktreeRow(snapshot, received, machinesnapshot.Worktree{
		Repository: "github.com/acme/app", Task: "task", Branch: "branch", AttentionReason: "a private raw reason",
	}, received, time.Hour)
	if !sanitized.NeedsAttention || sanitized.AttentionReason != "worktree requires attention" {
		t.Fatalf("sanitized attention = %+v", sanitized)
	}

	orphaned := worktreeRow(snapshot, received, machinesnapshot.Worktree{
		Repository: "github.com/acme/app", Task: "task", Branch: "branch", OwnerState: "orphaned",
	}, received, time.Hour)
	if !orphaned.NeedsAttention || orphaned.OwnerStatus != "orphaned" || orphaned.Status != "attention" ||
		orphaned.AttentionReason != "owner session is no longer active" {
		t.Fatalf("orphaned row = %+v", orphaned)
	}
}

func TestDQCovWorktreeRowHeartbeatPrefersNewestObservation(t *testing.T) {
	published := time.Unix(1000, 0).UTC()
	lastSeen := published.Add(5 * time.Minute)
	received := published.Add(time.Minute)
	snapshot := machinesnapshot.Snapshot{Machine: "vm", PublishedAt: published, LastSeenAt: lastSeen}

	row := worktreeRow(snapshot, received, machinesnapshot.Worktree{
		Repository: "github.com/acme/app", Task: "task", Branch: "branch",
	}, received, time.Hour)
	if !row.MachineSeenAt.Equal(lastSeen) {
		t.Fatalf("machine seen at = %v, want the later last_seen_at %v", row.MachineSeenAt, lastSeen)
	}
	if row.MachineStale {
		t.Fatalf("row with a fresh last_seen_at was marked stale: %+v", row)
	}

	older := machineHeartbeat(snapshot, published.Add(30*time.Second))
	if !older.Equal(lastSeen) {
		t.Fatalf("machineHeartbeat = %v, want %v", older, lastSeen)
	}
}
