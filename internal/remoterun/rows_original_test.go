package remoterun

import (
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

func TestMachineRowsTreatsZeroPublishedAtAsError(t *testing.T) {
	t.Parallel()
	entries := []remotestate.Entry{
		{
			Snapshot: remotestate.Snapshot{
				SchemaVersion: 1,
				Login:         "test",
				Machine:       "machine",
				PublishedAt:   time.Time{}, // zero time
			},
			Error: "",
		},
	}
	rows := MachineRows(entries, time.Now(), 24*time.Hour)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Error == "" {
		t.Fatalf("row.Error must be non-empty for zero PublishedAt")
	}
	if rows[0].Stale {
		t.Fatalf("row.Stale must be false when there's an error")
	}
	if rows[0].Age != "" {
		t.Fatalf("row.Age must be empty when there's an error")
	}
}

func TestMachineRowsSeenReflectsLastSeen(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	oldPublish := now.Add(-48 * time.Hour)
	freshSeen := now.Add(-1 * time.Hour)
	entries := []remotestate.Entry{
		{
			Snapshot: remotestate.Snapshot{
				SchemaVersion: 1,
				Login:         "alice",
				Machine:       "laptop",
				PublishedAt:   oldPublish,
				LastSeenAt:    freshSeen,
			},
		},
	}
	rows := MachineRows(entries, now, 24*time.Hour)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.Error != "" {
		t.Fatalf("row.Error = %q, want none", row.Error)
	}
	if row.Stale {
		t.Fatalf("row.Stale = true, want false: fresh claim activity (1h) must beat the 24h stale window even though PublishedAt is 48h old")
	}
	if row.Age != "2d" {
		t.Fatalf("row.Age (PUBLISHED) = %q, want the raw publish age 2d", row.Age)
	}
	if row.Seen != "1h" {
		t.Fatalf("row.Seen = %q, want the effective-heartbeat age 1h", row.Seen)
	}
	if !row.SeenAt.Equal(freshSeen) {
		t.Fatalf("row.SeenAt = %v, want %v", row.SeenAt, freshSeen)
	}
}

func TestMachineRowsZeroPublishedButLastSeenIsNotError(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	entries := []remotestate.Entry{
		{
			Snapshot: remotestate.Snapshot{
				SchemaVersion: 1,
				Login:         "alice",
				Machine:       "laptop",
				PublishedAt:   time.Time{},
				LastSeenAt:    now.Add(-1 * time.Hour),
			},
		},
	}
	rows := MachineRows(entries, now, 24*time.Hour)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Error != "" {
		t.Fatalf("row.Error = %q, want none: zero PublishedAt with non-zero LastSeenAt must not be an error row", rows[0].Error)
	}
	if rows[0].Stale {
		t.Fatalf("row.Stale = true, want false: LastSeenAt is fresh")
	}
}

func TestCwCovMachineRowsClassifyEveryEntry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []remotestate.Entry{
		{Error: "decode failed", Snapshot: remotestate.Snapshot{Login: "acme", Machine: "broken"}},
		{Snapshot: remotestate.Snapshot{Login: "acme", Machine: "empty"}},
		{Snapshot: remotestate.Snapshot{
			Login: "acme", Machine: "old", PublishedAt: now.Add(-3 * time.Hour), LastSeenAt: now.Add(-time.Minute),
			WBVersion: "v1", Repositories: []remotestate.RepositoryState{{Repository: "acme/a"}},
			Worktrees: []remotestate.WorktreeState{{Task: "t", Repository: "acme/a", Branch: "b"}},
		}},
		{Snapshot: remotestate.Snapshot{
			Login: "acme", Machine: "fresh", LastSeenAt: now.Add(-time.Minute),
			Repositories: []remotestate.RepositoryState{{Repository: "acme/a"}, {Repository: "acme/b"}},
		}},
	}

	rows := MachineRows(entries, now, 30*time.Minute)
	if len(rows) != 4 {
		t.Fatalf("rows = %+v, want one per entry", rows)
	}
	if rows[0].Error != "decode failed" || rows[0].Key != "acme/broken" {
		t.Errorf("error row = %+v", rows[0])
	}
	if !strings.Contains(rows[1].Error, "no published_at") {
		t.Errorf("empty snapshot row = %+v, want a stated error", rows[1])
	}
	// The heartbeat is the later of publish and claim activity, and staleness
	// keys off it: a three-hour-old publish with a fresh claim is not stale.
	if rows[2].Age != "3h" || rows[2].Seen != "1m" || rows[2].Stale {
		t.Errorf("heartbeat row = %+v, want age=3h seen=1m not stale", rows[2])
	}
	if rows[2].Attention != 1 || rows[2].Worktrees != 1 || rows[2].WBVersion != "v1" {
		t.Errorf("heartbeat row counts = %+v", rows[2])
	}
	// A snapshot with only claim activity has no publish age at all.
	if rows[3].Age != "" || rows[3].Seen != "1m" || rows[3].Attention != 2 || rows[3].Stale {
		t.Errorf("claim-only row = %+v", rows[3])
	}

	// A zero stale window disables the rule entirely.
	for _, row := range MachineRows(entries, now, 0) {
		if row.Stale {
			t.Errorf("row %q is stale with the rule disabled", row.Key)
		}
	}
}

func TestCwCovHolderDescNamesTheCallersOwnLogin(t *testing.T) {
	t.Parallel()
	mine := remotestate.Claim{Login: "acme", Machine: "here"}
	if got := HolderDesc(mine, remotestate.Claim{Login: "acme", Machine: "there"}); got != "you on there" {
		t.Errorf("HolderDesc(same login, other machine) = %q", got)
	}
	if got := HolderDesc(mine, remotestate.Claim{Login: "other", Machine: "there"}); got != "other/there" {
		t.Errorf("HolderDesc(other login) = %q", got)
	}
	// The same machine is the bare holder key, not "you on".
	if got := HolderDesc(mine, remotestate.Claim{Login: "acme", Machine: "here"}); got != "acme/here" {
		t.Errorf("HolderDesc(same machine) = %q", got)
	}
}

func TestRemoteStatusDiagnosticsReportProviderMismatch(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	cfg := remotestate.Config{Provider: "hub", URL: "https://wb.example", Machine: "vm"}
	entries := []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "alice", Machine: "laptop", PublishedAt: now, RemoteStore: "git:team/wb-state"}}}
	rows := MachineRows(entries, now, 24*time.Hour)
	diagnostics := buildStatusDiagnostics(cfg, entries, rows, now)
	if diagnostics.Provider != "hub" || diagnostics.Store != "hub:https://wb.example" || len(diagnostics.Mismatches) != 1 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if !strings.Contains(diagnostics.Mismatches[0], "alice/laptop publishes via git:team/wb-state") {
		t.Fatalf("mismatch = %q", diagnostics.Mismatches[0])
	}
}

func TestCwCovFindSnapshotHolderStaleAndHeartbeatPhrase(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	machines := []remotestate.Entry{
		{Error: "broken", Snapshot: remotestate.Snapshot{Login: "acme", Machine: "one"}},
		{Snapshot: remotestate.Snapshot{
			Login: "acme", Machine: "two", PublishedAt: now.Add(-4 * time.Hour), LastSeenAt: now.Add(-2 * time.Hour),
		}},
	}
	if _, ok := findSnapshot(machines, "acme", "one"); ok {
		t.Error("a corrupt snapshot must not be mistaken for silence")
	}
	snapshot, ok := findSnapshot(machines, "acme", "two")
	if !ok || snapshot.Machine != "two" {
		t.Fatalf("findSnapshot = (%+v, %t)", snapshot, ok)
	}
	if !HolderStale(machines, "acme", "absent", now, time.Hour) {
		t.Error("a holder with no snapshot must be stale")
	}
	if !HolderStale(machines, "acme", "two", now, time.Hour) {
		t.Error("a holder whose heartbeat is older than the window must be stale")
	}
	if HolderStale(machines, "acme", "two", now, 0) {
		t.Error("a zero window disables the staleness rule")
	}
	if HolderStale(machines, "acme", "two", now, 3*time.Hour) {
		t.Error("a heartbeat inside the window must be fresh")
	}
	if got := HeartbeatPhrase(machines, "acme", "absent", now, "never published"); got != "never published" {
		t.Errorf("missing-snapshot phrase = %q", got)
	}
	if got := HeartbeatPhrase(machines, "acme", "two", now, "never published"); got != "2h ago" {
		t.Errorf("heartbeat phrase = %q, want the effective heartbeat age", got)
	}
}
