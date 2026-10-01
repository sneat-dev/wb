package fleet

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// sentinelSources feeds every source a distinctive sentinel in every string
// field it has, then restores clean values to the few fields the metadata
// field set allows. A sentinel that reaches the document therefore names the
// source field that leaked.
func sentinelSources() *fakeSources {
	repo := filled[discover.Repo]()
	repo.Org, repo.Name, repo.Host, repo.DefaultBranch = "acme", "widgets", "github.com", "main"

	linked := filled[LinkedWorktree]()
	path := linked.Path // a sentinel path: it must never reach the document
	linked.Branch = "feature/a"
	record := filled[WorktreeRecord]()
	record.Task, record.Branch = "task-a", "feature/a"
	record.CreatedAt = newClock().Now()

	ref := filled[BranchRef]()
	ref.Name, ref.Scope, ref.Upstream, ref.Ahead, ref.Behind = "feature/a", BranchLocal, "origin/feature/a", 2, 3

	binding := filled[worktrees.RegisteredPullRequestBinding]()
	binding.Task, binding.Repository, binding.PullRequest, binding.URL = "task-a", "acme/widgets", 7, "https://github.com/acme/widgets/pull/7"

	view := filled[session.View]()
	view.WBSessionID, view.Runtime, view.Model, view.State = "wbs-1", "claude", "opus", session.StateLive

	run := filled[agents.Result]()
	run.AgentID, run.State, run.Repository = "agt-1", agents.StateRunning, "acme/widgets"
	run.Resolved.Harness, run.Resolved.Model = "codex", "gpt"

	// The remote entry is built by the filler like the rest, so every field its
	// types have, and gain later, carries a sentinel except the few the
	// document may show.
	entry := filled[remotestate.Entry]()
	published, _ := time.Parse(time.RFC3339, remotePublish)
	entry.Error = ""
	entry.Snapshot.Login, entry.Snapshot.Machine, entry.Snapshot.WBVersion, entry.Snapshot.PublishedAt = "someone", "desktop", "v0.9.0", published
	entry.Snapshot.KnownRepositories = []string{"acme/gadgets"}
	state := &entry.Snapshot.Worktrees[0]
	state.Task, state.Stream, state.Repository, state.Branch = "task-x", "stream-x", "acme/gadgets", "feature/x"
	state.Lifecycle, state.OwnerState = "active", "active"
	state.PullRequest.Number, state.PullRequest.State, state.PullRequest.URL = 3, "OPEN", "https://github.com/acme/gadgets/pull/3"

	return &fakeSources{
		repos:     []discover.Repo{repo},
		branch:    "main",
		worktrees: map[string][]LinkedWorktree{"acme/widgets": {linked}},
		records:   map[string]WorktreeRecord{path: record},
		branches:  map[string][]BranchRef{"acme/widgets": {ref}},
		bindings:  []worktrees.RegisteredPullRequestBinding{binding},
		sessions:  []session.View{view},
		runs:      []agents.Result{run},
		remote:    []remotestate.Entry{entry},
	}
}

// TestDocumentCarriesNoSourceFieldOutsideTheMetadataSet feeds every source a
// sentinel in each forbidden field, for local state and for a remote snapshot,
// and requires the marshalled document to hold none of them, while the allowed
// fields do arrive (so the test is not vacuous).
func TestDocumentCarriesNoSourceFieldOutsideTheMetadataSet(t *testing.T) {
	t.Parallel()
	snapshotter, _ := newSnapshotter(sentinelSources().collectors(), nil)
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	body := server.get("/api/v1/cockpit/fleet", nil).Body.String()
	if strings.Contains(body, strconv.Itoa(sentinelNumber)) {
		t.Fatalf("the document carries a sentinel number from a source field: %s", body)
	}
	if strings.Contains(body, sentinel) {
		start := strings.Index(body, sentinel)
		t.Fatalf("the document carries a forbidden source field: ...%s...", body[start:min(len(body), start+60)])
	}
	for _, want := range []string{
		`"task-a"`, `"feature/a"`, `"acme/widgets"`, `"wbs-1"`, `"agt-1"`, `"codex"`, `"task-x"`, `"stream-x"`, `"acme/gadgets"`,
		`"desktop"`, `"v0.9.0"`, `https://github.com/acme/gadgets/pull/3`, `https://github.com/acme/widgets/pull/7`, `"main"`, `"origin/feature/a"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the document lacks the allowed value %s: %s", want, body)
		}
	}
}

// TestDocumentFieldsAreExactlyTheMetadataFieldSet pins the closed field list
// of cockpit#req:anonymous-local-reads-metadata-only: a field added to a
// document type must be added here deliberately.
func TestDocumentFieldsAreExactlyTheMetadataFieldSet(t *testing.T) {
	t.Parallel()
	entry := []string{"id", "machine", "machine_id", "route", "observed_at"}
	want := map[string][]string{
		"Document":       {"schema_version", "snapshot_at", "warming_up", "repositories_total", "repositories_scanned", "diagnostics", "error", "code_index_provider", "machines", "repositories", "worktrees", "branches", "pull_requests", "agents", "agents_truncated"},
		"Machine":        append([]string{"wb_version", "repository_count", "worktree_count"}, entry...),
		"Repository":     append([]string{"host", "name", "default_branch", "worktree_count", "local_branch_count", "remote_branch_count", "open_pull_request_count", "active_agent_count", "error", "code_index"}, entry...),
		"Worktree":       append([]string{"repository", "task", "stream", "branch", "lifecycle", "owner_state", "last_activity_at", "code_index"}, entry...),
		"Branch":         append([]string{"repository", "name", "scope", "task", "worktree", "upstream", "ahead", "behind", "upstream_gone", "last_activity_at"}, entry...),
		"PullRequest":    append([]string{"repository", "worktree", "branch", "number", "state", "url"}, entry...),
		"CodeIndex":      {"indexer", "state", "behind", "receipt_at", "statistics"},
		"CodeStatistics": {"indexed", "files", "symbols", "edges", "kinds", "error"},
		"KindCount":      {"kind", "count"},
		"Agent":          append([]string{"kind", "session_id", "run_id", "runtime", "model", "state", "repository"}, entry...),
	}
	for name, got := range map[string][]string{
		"Document": jsonFields(Document{}), "Machine": jsonFields(Machine{}), "Repository": jsonFields(Repository{}),
		"Worktree": jsonFields(Worktree{}), "Branch": jsonFields(Branch{}), "PullRequest": jsonFields(PullRequest{}), "Agent": jsonFields(Agent{}),
		"CodeIndex": jsonFields(CodeIndex{}), "CodeStatistics": jsonFields(CodeStatistics{}), "KindCount": jsonFields(KindCount{}),
	} {
		if !sameSet(got, want[name]) {
			t.Errorf("%s fields = %v, want exactly %v", name, got, want[name])
		}
	}
}
