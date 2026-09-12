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

// A real stream can have two stale projections at once: one member is behind
// its remote branch and genuinely lacks a PR, while another already has its
// branch PR on GitHub but the local stream record missed the receipt. Status
// must use each member worktree as repository context and stay strictly
// read-only; join is the explicit mutating recovery verb.
func TestStatusReadOnlyDiscoversEachMembersExistingPullRequest(t *testing.T) {
	engine, git, hub, _ := newTestEngine(t)
	const branch = "stream/incident-recovery"
	corePath := "/projects/datatug/datatug-core/.worktrees/incident-recovery"
	cliPath := "/projects/datatug/datatug-cli/.worktrees/incident-recovery"
	if _, err := engine.Store.Create(Stream{
		Name: "incident-recovery", Phase: PhaseOpen,
		Members: []Member{
			{Repository: "datatug/datatug-core", Role: RoleLibrary, Worktree: corePath, Branch: branch, Base: "main", PullRequestError: "historical non-fast-forward"},
			{Repository: "datatug/datatug-cli", Role: RoleConsumer, Worktree: cliPath, Branch: branch, Base: "main", PullRequestError: "historical create failure"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	hub.byBranch[cliPath+" "+branch] = PullRequest{
		Number: 242, URL: "https://github.com/datatug/datatug-cli/pull/242",
		Head: branch, Base: "main", Draft: true, State: "OPEN",
	}
	pushesBefore, createsBefore := len(git.pushed), len(hub.created)

	status, err := engine.Status(context.Background(), "incident-recovery")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(git.pushed) != pushesBefore || len(hub.created) != createsBefore {
		t.Fatalf("status mutated publication state: pushes %d->%d creates %d->%d", pushesBefore, len(git.pushed), createsBefore, len(hub.created))
	}
	var core, cli MemberStatus
	for _, member := range status.Members {
		switch member.Repository {
		case "datatug/datatug-core":
			core = member
		case "datatug/datatug-cli":
			cli = member
		}
	}
	if core.PullRequest != 0 || core.PullRequestRecovery != "wb stream join incident-recovery datatug/datatug-core" {
		t.Fatalf("core status = %#v, want a truthful missing-PR recovery", core)
	}
	if cli.PullRequest != 242 || cli.PullRequestURL != "https://github.com/datatug/datatug-cli/pull/242" || cli.PullRequestMissing != "" {
		t.Fatalf("CLI status = %#v, want existing PR #242 discovered in the CLI repository context", cli)
	}
	if cli.PullRequestRecovery != "wb stream join incident-recovery datatug/datatug-cli" {
		t.Fatalf("CLI recovery = %q, want join to persist the remotely discovered receipt", cli.PullRequestRecovery)
	}
}

func TestStatusBlocksMismatchedMemberPullRequestsWithoutARetryLoop(t *testing.T) {
	for _, testCase := range []struct {
		name string
		pr   PullRequest
		want string
	}{
		{name: "wrong base", pr: PullRequest{Number: 12, URL: "https://example.test/pull/12", Head: "stream/mismatch", Base: "release", State: "OPEN"}, want: "targets release"},
		{name: "wrong head", pr: PullRequest{Number: 13, URL: "https://example.test/pull/13", Head: "stream/other", Base: "main", State: "OPEN"}, want: "head stream/other"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			engine, git, hub, _ := newTestEngine(t)
			const worktree = "/projects/acme/app/.worktrees/mismatch"
			if _, err := engine.Store.Create(Stream{
				Name: "mismatch", Phase: PhaseOpen,
				Members: []Member{{
					Repository: "acme/app", Role: RoleLibrary, Worktree: worktree,
					Branch: "stream/mismatch", Base: "main", PullRequestError: "historical failure",
				}},
			}); err != nil {
				t.Fatal(err)
			}
			hub.byBranch[worktree+" stream/mismatch"] = testCase.pr
			pushesBefore, createsBefore := len(git.pushed), len(hub.created)

			status, err := engine.Status(context.Background(), "mismatch")
			if err != nil {
				t.Fatal(err)
			}
			member := status.Members[0]
			if member.PullRequestRecovery != "" || !strings.Contains(member.PullRequestBlocked, testCase.want) {
				t.Fatalf("member status = %#v, want mismatch blocked with no join retry", member)
			}
			if len(git.pushed) != pushesBefore || len(hub.created) != createsBefore {
				t.Fatalf("status mutated mismatch: pushes %d->%d creates %d->%d", pushesBefore, len(git.pushed), createsBefore, len(hub.created))
			}
		})
	}
}

func TestStatusLabelsPersistedPublicationFailureAsHistorical(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	const worktree = "/projects/acme/app/.worktrees/history"
	failureAt := time.Date(2026, 9, 12, 11, 17, 40, 0, time.UTC)
	failure := "push stream/history: exit status 1\nfull historical git transcript"
	if _, err := engine.Store.Create(Stream{
		Name: "history", Phase: PhaseOpen,
		Members: []Member{{
			Repository: "acme/app", Role: RoleLibrary, Worktree: worktree,
			Branch: "stream/history", Base: "main", PullRequestError: failure,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Store.EventLog("history").Append(Event{
		Timestamp: failureAt, Stream: "history", Verb: "stream start", Phase: "push",
		Repository: "acme/app", Outcome: "findings", Detail: failure,
	}); err != nil {
		t.Fatal(err)
	}

	status, err := engine.Status(context.Background(), "history")
	if err != nil {
		t.Fatal(err)
	}
	member := status.Members[0]
	if member.PullRequestMissing != "no open pull request is recorded or currently discoverable" {
		t.Fatalf("current finding = %q, want a current-state statement rather than the old transcript", member.PullRequestMissing)
	}
	if member.LastPublicationError == nil || member.LastPublicationError.OccurredAt == nil ||
		!member.LastPublicationError.OccurredAt.Equal(failureAt) || member.LastPublicationError.Detail != failure {
		t.Fatalf("historical publication error = %#v, want timestamped persisted evidence", member.LastPublicationError)
	}
}

func TestStatusDoesNotMisdatePersistedPublicationFailure(t *testing.T) {
	engine, _, _, _ := newTestEngine(t)
	const worktree = "/projects/acme/app/.worktrees/history"
	const persistedFailure = "push stream/history: exit status 1"
	if _, err := engine.Store.Create(Stream{
		Name: "history", Phase: PhaseOpen,
		Members: []Member{{
			Repository: "acme/app", Role: RoleLibrary, Worktree: worktree,
			Branch: "stream/history", Base: "main", PullRequestError: persistedFailure,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Store.EventLog("history").Append(Event{
		Timestamp: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC),
		Stream:    "history", Verb: "stream join", Phase: "push",
		Repository: "acme/app", Outcome: "findings", Detail: "a different later failure",
	}); err != nil {
		t.Fatal(err)
	}

	status, err := engine.Status(context.Background(), "history")
	if err != nil {
		t.Fatal(err)
	}
	failure := status.Members[0].LastPublicationError
	if failure == nil || failure.Detail != persistedFailure || failure.OccurredAt != nil {
		t.Fatalf("historical publication error = %#v, want undated persisted evidence", failure)
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
