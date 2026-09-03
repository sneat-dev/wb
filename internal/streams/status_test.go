package streams

import (
	"context"
	"strings"
	"testing"
	"time"
)

// AC: status-separates-linked-untagged-and-behind — one consumer holds a live
// link, the library has merged work with no tag, and a second consumer
// declares an older published version. All three are reported separately, are
// named per repository, and come from stream state after a session restart.
func TestStatusSeparatesLinkedUntaggedAndBehind(t *testing.T) {
	engine, git, hub, _ := newTestEngine(t)
	for _, repository := range []string{"acme/library", "acme/linked", "acme/behind"} {
		writeCanonical(t, engine.ProjectsRoot, repository, map[string]string{
			".github/workflows/ci.yml": cancellingWorkflow,
		})
	}
	started, err := engine.Start(context.Background(), StartOptions{
		Name:         "three-gaps",
		Repositories: []string{"acme/library", "acme/linked", "acme/behind"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	library, _ := started.Stream.Member("acme/library")
	linked, _ := started.Stream.Member("acme/linked")
	behind, _ := started.Stream.Member("acme/behind")

	writeFiles(t, library.Worktree, map[string]string{
		"backend/go.mod": "module github.com/acme/library/backend\n\ngo 1.27\n",
	})
	writeFiles(t, behind.Worktree, map[string]string{
		"backend/go.mod": "module github.com/acme/behind/backend\n\ngo 1.27\n\nrequire (\n\tgithub.com/acme/library/backend v0.4.0\n)\n",
	})
	git.tags[library.Worktree] = []string{"backend/v0.5.0", "backend/v0.4.0"}
	git.log[library.Worktree+" backend/v0.5.0..origin/main"] = []string{"feat(library): the merged but untagged change"}

	if _, err := engine.Store.Update("three-gaps", func(stream *Stream) error {
		for index := range stream.Members {
			if stream.Members[index].Repository != "acme/linked" {
				continue
			}
			stream.Members[index].Links = []Link{{
				Library:           library.Worktree,
				LibraryRepository: "acme/library",
				Mechanism:         MechanismGoWork,
				Identity:          "github.com/acme/library/backend",
				PreviousVersion:   "v0.5.0",
				ContentHash:       "abc123",
				CreatedAt:         time.Now().UTC(),
			}}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	hub.targeting[linked.Worktree+" stream/three-gaps"] = []PullRequest{
		{Number: 7, URL: "https://example.test/pull/7", Title: "agent work", Head: "agent/one", Base: "stream/three-gaps"},
	}

	// A fresh reader stands in for a session restart: nothing below is read
	// from the earlier in-memory result.
	restarted := &Engine{
		Store: OpenAt(engine.Store.Root), Git: git, GitHub: hub,
		Worktrees: engine.Worktrees, ProjectsRoot: engine.ProjectsRoot,
	}
	status, err := restarted.Status(context.Background(), "three-gaps")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if len(status.LinkedConsumers) != 1 || status.LinkedConsumers[0].Repository != "acme/linked" {
		t.Fatalf("linked consumers = %#v, want exactly acme/linked", status.LinkedConsumers)
	}
	if status.LinkedConsumers[0].Library != library.Worktree {
		t.Errorf("linked consumer does not name the library worktree it links to: %#v", status.LinkedConsumers[0])
	}
	if status.MergedUntagged == nil || status.MergedUntagged.Repository != "acme/library" {
		t.Fatalf("merged-untagged = %#v, want acme/library", status.MergedUntagged)
	}
	if len(status.MergedUntagged.Commits) != 1 {
		t.Errorf("merged-untagged commits = %v, want the one merged change", status.MergedUntagged.Commits)
	}
	if len(status.ConsumersBehind) != 1 || status.ConsumersBehind[0].Repository != "acme/behind" {
		t.Fatalf("consumers behind = %#v, want exactly acme/behind", status.ConsumersBehind)
	}
	if status.ConsumersBehind[0].Declared != "v0.4.0" || status.ConsumersBehind[0].Published != "v0.5.0" {
		t.Errorf("behind row = %#v, want v0.4.0 against published v0.5.0", status.ConsumersBehind[0])
	}
	if len(status.OpenAgentPullRequests) != 1 || status.OpenAgentPullRequests[0].Number != 7 {
		t.Fatalf("open agent pull requests = %#v, want the one targeting the stream branch", status.OpenAgentPullRequests)
	}
}

// REQ: stream-backlog-is-counted-by-patch-identity — N branches carrying one
// body of work are named as one cluster with their cardinality.
func TestStatusCollapsesPatchIdenticalBacklog(t *testing.T) {
	engine, git, _, _ := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	started, err := engine.Start(context.Background(), StartOptions{
		Name: "backlog", Repositories: []string{"acme/library"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	member := started.Stream.Members[0]
	// Real `git cherry -v` emits a distinct SHA per commit; two commits
	// carrying one body of work differ in SHA by construction and agree only
	// on their patch id. Feeding bare subjects — as this test used to — made
	// the assertion pass against a shape ExecGit never produces.
	git.notIn[member.Worktree+" stream/backlog origin/main"] = []Commit{
		{SHA: "35c480ed6e1e718a910d8aa617c4da94dd47557a", Subject: "feat: one body of work", PatchID: "9f1c2d"},
		{SHA: "430cff73657583ec4c18a0b2b94e738b50c5e04b", Subject: "feat: one body of work", PatchID: "9f1c2d"},
		{SHA: "5a1f0d2c9b8e7a6d5c4b3a2918f7e6d5c4b3a291", Subject: "fix: something else", PatchID: "7b3e10"},
	}
	status, err := engine.Status(context.Background(), "backlog")
	if err != nil {
		t.Fatal(err)
	}
	row := status.Members[0]
	if row.Unabsorbed != 3 {
		t.Errorf("unabsorbed = %d, want 3", row.Unabsorbed)
	}
	if len(row.UnabsorbedClusters) != 2 {
		t.Fatalf("subjects = %v, want two clusters", row.UnabsorbedClusters)
	}
	if !strings.Contains(row.UnabsorbedClusters[0], "×2 patch-identical") {
		t.Errorf("cluster is not named with its cardinality: %q", row.UnabsorbedClusters[0])
	}
}

// A gap WB could not establish is reported as unknown; an empty gap list must
// never be readable as "nothing is wrong".
func TestStatusReportsWhatItCouldNotEstablish(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	writeCanonical(t, engine.ProjectsRoot, "acme/library", map[string]string{
		".github/workflows/ci.yml": cancellingWorkflow,
	})
	if _, err := engine.Start(context.Background(), StartOptions{
		Name: "unknowns", Repositories: []string{"acme/library"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	status, err := engine.Status(context.Background(), "unknowns")
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Unknowns) == 0 {
		t.Fatal("a library publishing no discoverable identity must be reported as unknown")
	}
}

func TestVersionComparisonTreatsUnreadableVersionsAsNotBehind(t *testing.T) {
	for _, testCase := range []struct {
		declared, published string
		want                bool
	}{
		{"v0.5.0", "v0.5.0", true},
		{"v0.4.9", "v0.5.0", false},
		{"^1.2.3", "1.2.4", false},
		{"~1.3.0", "1.2.9", true},
		{"workspace:*", "1.2.3", true},
		{"v1.2.3-rc.1", "1.2.3", true},
	} {
		if got := versionAtLeast(testCase.declared, testCase.published); got != testCase.want {
			t.Errorf("versionAtLeast(%q, %q) = %t, want %t", testCase.declared, testCase.published, got, testCase.want)
		}
	}
}
