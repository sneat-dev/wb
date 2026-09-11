package machinesnapshot

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestResolveLatestIsIdempotentAndMonotonic(t *testing.T) {
	at := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	first := StoredSnapshot{Snapshot: validSnapshot(at), ReceivedAt: at.Add(time.Second), Digest: "first"}
	created, err := ResolveLatest(nil, first)
	if err != nil || !created.Updated || created.Current.Digest != "first" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	duplicate := first
	duplicate.ReceivedAt = at.Add(2 * time.Second)
	unchanged, err := ResolveLatest(&first, duplicate)
	if err != nil || unchanged.Updated || !unchanged.Current.ReceivedAt.Equal(first.ReceivedAt) {
		t.Fatalf("duplicate = %+v, %v", unchanged, err)
	}
	stale := first
	stale.Digest = "stale"
	stale.Snapshot.PublishedAt = at.Add(-time.Minute)
	if _, err := ResolveLatest(&first, stale); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("stale err = %v", err)
	}
	conflict := first
	conflict.Digest = "conflict"
	if _, err := ResolveLatest(&first, conflict); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("conflict err = %v", err)
	}
	newer := first
	newer.Digest = "newer"
	newer.Snapshot.PublishedAt = at.Add(time.Minute)
	updated, err := ResolveLatest(&first, newer)
	if err != nil || !updated.Updated || updated.Current.Digest != "newer" {
		t.Fatalf("newer = %+v, %v", updated, err)
	}
}

func TestSnapshotValidateBoundsHostedSchema(t *testing.T) {
	snapshot := validSnapshot(time.Now().UTC())
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	snapshot.Worktrees[0].PullRequest.URL = "http://example.test/pull/1"
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("HTTP PR URL err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Worktrees = make([]Worktree, MaxWorktrees+1)
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("oversized worktrees err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Repositories = []string{"github.com/zeta/tools", "github.com/acme/widgets"}
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("unsorted repositories err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Repositories = []string{"acme/widgets"}
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("noncanonical repository err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Repositories = []string{"github.com/acme/.github", "github.com/acme/widgets"}
	snapshot.Worktrees[0].Repository = "acme/.github"
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("a repository named .github is valid on GitHub: %v", err)
	}
	for _, name := range []string{".", "..", ""} {
		snapshot = validSnapshot(time.Now().UTC())
		snapshot.Repositories = []string{"github.com/acme/" + name}
		if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("repository name %q err = %v", name, err)
		}
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Repositories = []string{"github.com/.acme/widgets"}
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("dotted owner err = %v", err)
	}
	snapshot = validSnapshot(time.Now().UTC())
	snapshot.Worktrees[0].AttentionReason = "/Users/alice/private output"
	if err := snapshot.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("unsafe attention err = %v", err)
	}
}

func TestSnapshotKeyIsStableFlatAndValidatesIdentity(t *testing.T) {
	first, err := SnapshotKey("alice", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	second, err := SnapshotKey("alice", "laptop")
	if err != nil || first != second || !strings.HasPrefix(first, "machine_") || strings.Contains(first, "alice") {
		t.Fatalf("keys = %q/%q, err = %v", first, second, err)
	}
	if _, err := SnapshotKey("../alice", "laptop"); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("invalid identity err = %v", err)
	}
}

func validSnapshot(at time.Time) Snapshot {
	return Snapshot{
		SchemaVersion: SchemaVersion, Login: "alice", Machine: "laptop", PublishedAt: at,
		Repositories: []string{"github.com/acme/widgets"},
		Worktrees: []Worktree{{
			Task: "dashboard", Repository: "acme/widgets", Branch: "feature/dashboard",
			PullRequest: &PullRequest{Number: 1, URL: "https://github.com/acme/widgets/pull/1"},
		}},
	}
}
