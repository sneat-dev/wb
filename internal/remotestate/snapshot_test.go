package remotestate

import (
	"reflect"
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
		{Repository: "acme/dirty", Path: "/p/dirty", Status: gitops.RepoStatus{Modified: []string{"a.go"}, Untracked: []string{"b.txt"}, Conflicted: []string{"c.md"}, Stashed: []string{"stash@{0}"}}, Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main", Ahead: 1, Behind: 2}},
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

	// Field-level assertions for acme/dirty
	dirtyIdx := -1
	for i, r := range snap.Repositories {
		if r.Repository == "acme/dirty" {
			dirtyIdx = i
			break
		}
	}
	if dirtyIdx < 0 {
		t.Fatal("acme/dirty not found")
	}
	expected := RepositoryState{
		Repository:    "acme/dirty",
		Path:          "/p/dirty",
		Status:        "attention",
		Summary:       "1 modified file, 1 untracked file, 1 conflict, 1 stash entry; main is 1 ahead, 2 behind origin/main",
		Branch:        "main",
		Upstream:      "origin/main",
		Ahead:         1,
		Behind:        2,
		Modified:      []string{"a.go"},
		Untracked:     []string{"b.txt"},
		Conflicted:    []string{"c.md"},
		UnpushedCount: 0,
		Stashed:       []string{"stash@{0}"},
	}
	if !reflect.DeepEqual(snap.Repositories[dirtyIdx], expected) {
		t.Fatalf("acme/dirty mismatch:\n got: %+v\nwant: %+v", snap.Repositories[dirtyIdx], expected)
	}

	// Field-level assertion for acme/broken
	brokenIdx := -1
	for i, r := range snap.Repositories {
		if r.Repository == "acme/broken" {
			brokenIdx = i
			break
		}
	}
	if brokenIdx < 0 {
		t.Fatal("acme/broken not found")
	}
	if snap.Repositories[brokenIdx].Error != "git status: boom" {
		t.Fatalf("acme/broken Error = %q, want %q", snap.Repositories[brokenIdx].Error, "git status: boom")
	}
	if snap.Repositories[brokenIdx].Path != "/p/broken" {
		t.Fatalf("acme/broken Path = %q, want %q", snap.Repositories[brokenIdx].Path, "/p/broken")
	}
}

func TestBuildRedactsUnpushedSubjectsToCounts(t *testing.T) {
	attribution := []gitops.UnpushedBranch{{Branch: "feature", Worktree: "/p/feature", Commits: []string{"abc feat", "def fix"}}}
	repos := []RepositoryInput{{Repository: "acme/x", Path: "/p/x", Status: gitops.RepoStatus{Unpushed: []string{"abc feat", "def fix"}, UnpushedBranches: attribution}, Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main", Ahead: 2}}}

	full := Build(identity(), repos, nil, RedactNone)
	if len(full.Repositories[0].Unpushed) != 2 || full.Repositories[0].UnpushedCount != 2 {
		t.Fatalf("subjects mode: Unpushed=%v UnpushedCount=%d", full.Repositories[0].Unpushed, full.Repositories[0].UnpushedCount)
	}
	if !reflect.DeepEqual(full.Repositories[0].UnpushedBranches, attribution) {
		t.Fatalf("subjects mode: UnpushedBranches=%+v, want %+v", full.Repositories[0].UnpushedBranches, attribution)
	}
	redacted := Build(identity(), repos, nil, RedactUnpushed)
	if redacted.Repositories[0].Unpushed != nil || redacted.Repositories[0].UnpushedBranches != nil || redacted.Repositories[0].UnpushedCount != 2 {
		t.Fatalf("counts mode: Unpushed=%v UnpushedBranches=%+v UnpushedCount=%d", redacted.Repositories[0].Unpushed, redacted.Repositories[0].UnpushedBranches, redacted.Repositories[0].UnpushedCount)
	}
}

func TestBuildSummarisesTrackingOnlyAttention(t *testing.T) {
	repos := []RepositoryInput{
		{Repository: "acme/ahead", Path: "/p/ahead", Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main", Ahead: 2}},
		{Repository: "acme/noup", Path: "/p/noup", Tracking: gitops.TrackingState{Branch: "feature", Configured: true}},
		{Repository: "acme/noup-unconfigured", Path: "/p/noup-unconfigured", Tracking: gitops.TrackingState{Branch: "feature", Configured: false, Upstream: ""}},
	}
	snap := Build(identity(), repos, nil, RedactNone)

	for _, r := range snap.Repositories {
		if r.Repository == "acme/ahead" {
			expected := "main is 2 ahead, 0 behind origin/main"
			if r.Summary != expected {
				t.Fatalf("acme/ahead Summary = %q, want %q", r.Summary, expected)
			}
		}
		if r.Repository == "acme/noup" {
			expected := "feature tracks an upstream that no longer resolves"
			if r.Summary != expected {
				t.Fatalf("acme/noup Summary = %q, want %q", r.Summary, expected)
			}
		}
		if r.Repository == "acme/noup-unconfigured" {
			expected := "feature has no upstream"
			if r.Summary != expected {
				t.Fatalf("acme/noup-unconfigured Summary = %q, want %q", r.Summary, expected)
			}
		}
	}
}

func TestBuildSummaryOmitsCleanTracking(t *testing.T) {
	repos := []RepositoryInput{
		{Repository: "acme/dirty", Path: "/p/dirty", Status: gitops.RepoStatus{Modified: []string{"a.go"}}, Tracking: gitops.TrackingState{Branch: "main", Upstream: "origin/main"}},
	}
	snap := Build(identity(), repos, nil, RedactNone)

	if len(snap.Repositories) != 1 {
		t.Fatalf("expected 1 repository, got %d", len(snap.Repositories))
	}
	expected := "1 modified file"
	if snap.Repositories[0].Summary != expected {
		t.Fatalf("Summary = %q, want %q", snap.Repositories[0].Summary, expected)
	}
}

func TestBuildCarriesWorktrees(t *testing.T) {
	wts := []worktrees.ListResult{
		{Task: "task-7", Repository: "acme/z", Branch: "agent/task-7", HeadSHA: "abc123", WorktreeDir: "/wt/task-7/acme/z", OwnerState: "active"},
		{Task: "task-7", Repository: "acme/a", Branch: "agent/task-7", HeadSHA: "abc123", WorktreeDir: "/wt/task-7/acme/a", OwnerState: "orphaned"},
		{Task: "task-1", Repository: "acme/m", Branch: "agent/task-1", HeadSHA: "abc123", WorktreeDir: "/wt/task-1/acme/m", OwnerState: "unknown"},
	}
	snap := Build(identity(), nil, wts, RedactNone)
	if len(snap.Worktrees) != 3 {
		t.Fatalf("expected 3 worktrees, got %d", len(snap.Worktrees))
	}
	// Verify sort order: task-1 first, then task-7 sorted by repository, and
	// that each worktree's owner state (active/orphaned/unknown liveness of
	// its owning session) survives Build unchanged.
	expected := []WorktreeState{
		{Task: "task-1", Repository: "acme/m", Branch: "agent/task-1", HeadSHA: "abc123", Dir: "/wt/task-1/acme/m", OwnerState: "unknown"},
		{Task: "task-7", Repository: "acme/a", Branch: "agent/task-7", HeadSHA: "abc123", Dir: "/wt/task-7/acme/a", OwnerState: "orphaned"},
		{Task: "task-7", Repository: "acme/z", Branch: "agent/task-7", HeadSHA: "abc123", Dir: "/wt/task-7/acme/z", OwnerState: "active"},
	}
	if !reflect.DeepEqual(snap.Worktrees, expected) {
		t.Fatalf("Worktrees mismatch:\n got: %+v\nwant: %+v", snap.Worktrees, expected)
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
