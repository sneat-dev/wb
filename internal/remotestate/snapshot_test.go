package remotestate

import (
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func identity() Snapshot {
	return Snapshot{Login: "alice", Machine: "laptop", PublishedAt: time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC), WBVersion: "v1.2.3", ProjectsRoot: "/home/alice/projects"}
}

func TestBuildListsOnlyNonCleanRepositoriesAndCountsAll(t *testing.T) {
	repos := []RepositoryInput{
		{Repository: "acme/clean", Path: "/p/clean", Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main"}},
		{Repository: "acme/dirty", Path: "/p/dirty", Status: gitops.RepoStatus{Modified: []string{"a.go"}}, Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main"}},
		{Repository: "acme/ahead", Path: "/p/ahead", Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main", Ahead: 2}},
		{Repository: "acme/noup", Path: "/p/noup", Tracking: gitops.TrackingState{Branch: "feature", Configured: true}},
		{Repository: "acme/broken", Path: "/p/broken", Err: errOops},
	}
	snap := Build(identity(), repos, nil, RedactNone)

	if snap.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", snap.SchemaVersion, SchemaVersion)
	}
	if snap.RepositoriesScanned != 5 {
		t.Fatalf("RepositoriesScanned = %d, want 5", snap.RepositoriesScanned)
	}
	got := make([]string, 0, len(snap.Repositories))
	for _, r := range snap.Repositories {
		got = append(got, r.Repository+":"+r.Status)
	}
	want := "acme/ahead:attention acme/broken:error acme/dirty:attention acme/noup:attention"
	if strings.Join(got, " ") != want {
		t.Fatalf("repositories = %q, want %q (sorted by slug, clean omitted)", strings.Join(got, " "), want)
	}
	if snap.Key() != "alice/laptop" {
		t.Fatalf("Key() = %q", snap.Key())
	}
}

func TestBuildRedactsUnpushedSubjectsToCounts(t *testing.T) {
	repos := []RepositoryInput{{Repository: "acme/x", Path: "/p/x", Status: gitops.RepoStatus{Unpushed: []string{"abc feat", "def fix"}}, Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main", Ahead: 2}}}

	full := Build(identity(), repos, nil, RedactNone)
	if len(full.Repositories[0].Unpushed) != 2 || full.Repositories[0].UnpushedCount != 2 {
		t.Fatalf("subjects mode: Unpushed=%v UnpushedCount=%d", full.Repositories[0].Unpushed, full.Repositories[0].UnpushedCount)
	}
	redacted := Build(identity(), repos, nil, RedactUnpushed)
	if redacted.Repositories[0].Unpushed != nil || redacted.Repositories[0].UnpushedCount != 2 {
		t.Fatalf("counts mode: Unpushed=%v UnpushedCount=%d", redacted.Repositories[0].Unpushed, redacted.Repositories[0].UnpushedCount)
	}
}

func TestBuildCarriesWorktrees(t *testing.T) {
	wts := []worktrees.ListResult{{Task: "task-7", Repository: "acme/x", Branch: "agent/task-7", HeadSHA: "abc123", WorktreeDir: "/wt/task-7/acme/x"}}
	snap := Build(identity(), nil, wts, RedactNone)
	if len(snap.Worktrees) != 1 || snap.Worktrees[0] != (WorktreeState{Task: "task-7", Repository: "acme/x", Branch: "agent/task-7", HeadSHA: "abc123", Dir: "/wt/task-7/acme/x"}) {
		t.Fatalf("Worktrees = %+v", snap.Worktrees)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	snap := Build(identity(), []RepositoryInput{{Repository: "acme/x", Path: "/p/x", Status: gitops.RepoStatus{Stashed: []string{"stash@{0}"}}}}, nil, RedactNone)
	data, err := Encode(snap)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.Key() != snap.Key() || !back.PublishedAt.Equal(snap.PublishedAt) || len(back.Repositories) != 1 || back.Repositories[0].Stashed[0] != "stash@{0}" {
		t.Fatalf("round trip mismatch: %+v", back)
	}
}

func TestDecodeRejectsNewerSchema(t *testing.T) {
	_, err := Decode([]byte("schema_version: 99\nlogin: a\nmachine: b\n"))
	if err == nil || !strings.Contains(err.Error(), "schema_version 99") {
		t.Fatalf("err = %v, want newer-schema error", err)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode([]byte("{not yaml")); err == nil {
		t.Fatal("expected YAML error")
	}
}

var errOops = errString("git status: boom")

type errString string

func (e errString) Error() string { return string(e) }
