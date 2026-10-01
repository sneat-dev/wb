package fleet

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit/machinemetrics"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/herdr"
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
	// The view's process id is the sentinel number, as is the declared owner's of
	// the worktree record, so the session is linked to the worktree; the harness
	// session id is the one herdr's fake agent carries.
	view.NativeHarnessID, view.StartedAt = "hid-1", newClock().Now()
	record.OwnerPID = view.PID

	run := filled[agents.Result]()
	run.AgentID, run.State, run.Repository = "agt-1", agents.StateRunning, "acme/widgets"
	run.Resolved.Harness, run.Resolved.Model = "codex", "gpt"
	run.Worktree, run.Branch, run.StartedAt = "task-a", "feature/a", newClock().Now()
	finished := newClock().Now()
	failedRun := run
	failedRun.AgentID, failedRun.State, failedRun.FinishedAt = "agt-2", agents.StateFailed, &finished
	two := 2
	failedRun.ExitCode = &two

	// herdr's fake agent has a sentinel in every field but its status and the
	// harness session id that joins it to the session.
	herdrAgent := filled[herdr.Agent]()
	herdrAgent.Status = herdr.StatusBlocked
	herdrAgent.Session = &herdr.AgentSession{Agent: sentinel + "agent", Kind: "id", Source: sentinel + "source", Value: "hid-1"}

	// The remote entry is built by the filler like the rest, so every field its
	// types have, and gain later, carries a sentinel except the few the
	// document may show.
	entry := filled[remotestate.Entry]()
	published, _ := time.Parse(time.RFC3339, remotePublish)
	entry.Error = ""
	entry.Snapshot.Login, entry.Snapshot.Machine, entry.Snapshot.WBVersion, entry.Snapshot.PublishedAt = "someone", "desktop", "v0.9.0", published
	// The machine's hardware facts are fields the document may show.
	entry.Snapshot.OS, entry.Snapshot.Arch, entry.Snapshot.CPUCount, entry.Snapshot.BootTime = "linux", "arm64", 8, published
	entry.Snapshot.KnownRepositories = []string{"acme/gadgets"}
	state := &entry.Snapshot.Worktrees[0]
	state.Task, state.Stream, state.Repository, state.Branch = "task-x", "stream-x", "acme/gadgets", "feature/x"
	state.Lifecycle, state.OwnerState = "working", "orphaned"
	state.PullRequest.Number, state.PullRequest.State, state.PullRequest.URL = 3, "OPEN", "https://github.com/acme/gadgets/pull/3"

	return &fakeSources{
		repos:     []discover.Repo{repo},
		branch:    "main",
		worktrees: map[string][]LinkedWorktree{"acme/widgets": {linked}},
		records:   map[string]WorktreeRecord{path: record},
		branches:  map[string][]BranchRef{"acme/widgets": {ref}},
		bindings:  []worktrees.RegisteredPullRequestBinding{binding},
		sessions:  []session.View{view},
		runs:      []agents.Result{run, failedRun},
		remote:    []remotestate.Entry{entry},
		activity:  HerdrActivity{Open: func() (HerdrLister, error) { return fakeHerdr{agents: []herdr.Agent{herdrAgent}}, nil }},
	}
}

// TestDocumentCarriesNoSourceFieldOutsideTheMetadataSet feeds every source a
// sentinel in each forbidden field, for local state and for a remote snapshot,
// and requires the marshalled document to hold none of them, while the allowed
// fields do arrive (so the test is not vacuous).
func TestDocumentCarriesNoSourceFieldOutsideTheMetadataSet(t *testing.T) {
	t.Parallel()
	// The projects root is a sentinel path: it is compared and statted, never
	// emitted, and the metrics route must not carry it either.
	snapshotter, _ := newSnapshotter(sentinelSources().collectors(), func(options *Options) {
		options.ProjectsRoot = "/" + sentinel + "projects-root"
		options.Sampler = filledSampler(t, &countingSource{}, 3)
	})
	refreshAndSettle(t, snapshotter)
	server := newCockpitServer(t, snapshotter)
	body := server.get("/api/v1/cockpit/fleet", nil).Body.String()
	// The metrics of every machine in the document are served by their own
	// route (a local history, and none for the machines of snapshots), so the
	// same checks cover it, and its payload is the fixed field set and nothing more.
	for _, machine := range snapshotter.Document().Machines {
		recorder := server.get(metricsURL+machine.ID, nil)
		if recorder.Code != 200 {
			t.Fatalf("metrics route = %d %s", recorder.Code, recorder.Body.String())
		}
		var decoded struct {
			Samples []map[string]any `json:"samples"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		for _, sample := range decoded.Samples {
			keys := make([]string, 0, len(sample))
			for key := range sample {
				keys = append(keys, key)
			}
			if !sameSet(keys, []string{"load1", "memory_used_bytes", "memory_total_bytes", "disk_free_bytes", "disk_total_bytes", "sampled_at"}) {
				t.Errorf("a metrics sample carries %v", keys)
			}
		}
		if machine.Route == RouteLocal && len(decoded.Samples) != 3 {
			t.Errorf("the local machine has %d samples, want 3 (the test would be vacuous)", len(decoded.Samples))
		}
		body += recorder.Body.String()
		// A forwarded request has no anonymous reading: it gets no data at all.
		if forwarded := server.get(metricsURL+machine.ID, nil, "X-Forwarded-For", "203.0.113.9"); forwarded.Code != 401 || strings.Contains(forwarded.Body.String(), "samples") {
			t.Errorf("a forwarded metrics request = %d %s, want 401 and no data", forwarded.Code, forwarded.Body.String())
		}
	}
	// The branches are served by their own route, so the same checks cover it.
	for _, repository := range snapshotter.Document().Repositories {
		recorder := server.get("/api/v1/cockpit/branches?repository="+repository.ID, nil)
		if recorder.Code != 200 {
			t.Fatalf("branches route for a %s repository = %d %s", repository.Route, recorder.Code, recorder.Body.String())
		}
		body += recorder.Body.String()
	}
	// The export envelope (cockpit-views#req:cockpit-export-verb) is built from the
	// same document and sampler, so the same checks cover both of its shapes.
	for _, metricsOnly := range []bool{false, true} {
		exported, err := json.Marshal(snapshotter.Export(metricsOnly))
		if err != nil {
			t.Fatal(err)
		}
		body += string(exported)
	}
	if strings.Contains(body, strconv.Itoa(sentinelNumber)) {
		t.Fatalf("the document carries a sentinel number from a source field: %s", body)
	}
	if strings.Contains(body, sentinel) {
		start := strings.Index(body, sentinel)
		t.Fatalf("the document carries a forbidden source field: ...%s...", body[start:min(len(body), start+60)])
	}
	for _, want := range []string{
		`"task-a"`, `"feature/a"`, `"acme/widgets"`, `"wbs-1"`, `"agt-1"`, `"codex"`, `"task-x"`, `"stream-x"`, `"acme/gadgets"`,
		`"desktop"`, `"v0.9.0"`, `"activity":"blocked"`, `"exit_code":2`, `"finished_at"`, `"started_at"`, `"os":"linux"`, `"arch":"arm64"`, `"cpu_count":8`, `"owner_state":"orphaned"`, `"lifecycle":"working"`, `"refresh_interval_seconds":60`, `"remote_url_web":"https://github.com/acme/widgets"`, `https://github.com/acme/gadgets/pull/3`, `https://github.com/acme/widgets/pull/7`, `"main"`, `"origin/feature/a"`,
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
		"Document":         {"schema_version", "snapshot_at", "warming_up", "repositories_total", "repositories_scanned", "diagnostics", "error", "code_index_provider", "refresh_interval_seconds", "machines", "repositories", "worktrees", "pull_requests", "agents", "agents_truncated", "pull_requests_throttled"},
		"Machine":          append([]string{"wb_version", "repository_count", "worktree_count", "os", "arch", "cpu_count", "boot_time"}, entry...),
		"Repository":       append([]string{"host", "name", "default_branch", "worktree_count", "local_branch_count", "remote_branch_count", "open_pull_request_count", "active_agent_count", "error", "last_activity_at", "remote_url_web", "code_index"}, entry...),
		"Worktree":         append([]string{"repository", "name", "task", "stream", "branch", "lifecycle", "owner_state", "last_activity_at", "ahead", "behind", "upstream_gone", "has_upstream", "code_index"}, entry...),
		"Branch":           append([]string{"repository", "name", "scope", "task", "worktree", "upstream", "ahead", "behind", "upstream_gone", "last_activity_at"}, entry...),
		"PullRequest":      append([]string{"repository", "worktree", "branch", "number", "state", "url", "mergeable", "checks_total", "checks_passed", "checks_failed", "checks_skipped", "checks_pending", "checks_green", "failed_check", "checked_at"}, entry...),
		"BranchesResponse": {"repository", "branches", "reason"},
		"CodeIndex":        {"indexer", "state", "behind", "receipt_at", "statistics"},
		"CodeStatistics":   {"indexed", "files", "symbols", "edges", "kinds", "error"},
		"KindCount":        {"kind", "count"},
		"MetricsResponse":  {"machine", "route", "fetched_at", "samples", "reason"},
		"Sample":           {"cpu_percent", "load1", "memory_used_bytes", "memory_total_bytes", "disk_free_bytes", "disk_total_bytes", "sampled_at"},
		"Agent":            append([]string{"kind", "session_id", "run_id", "runtime", "model", "state", "activity", "repository", "task", "worktrees", "started_at", "finished_at", "exit_code"}, entry...),
	}
	for name, got := range map[string][]string{
		"Document": jsonFields(Document{}), "Machine": jsonFields(Machine{}), "Repository": jsonFields(Repository{}),
		"Worktree": jsonFields(Worktree{}), "Branch": jsonFields(Branch{}), "PullRequest": jsonFields(PullRequest{}), "Agent": jsonFields(Agent{}),
		"BranchesResponse": jsonFields(BranchesResponse{}), "MetricsResponse": jsonFields(MetricsResponse{}), "Sample": jsonFields(machinemetrics.Sample{}), "CodeIndex": jsonFields(CodeIndex{}), "CodeStatistics": jsonFields(CodeStatistics{}), "KindCount": jsonFields(KindCount{}),
	} {
		if !sameSet(got, want[name]) {
			t.Errorf("%s fields = %v, want exactly %v", name, got, want[name])
		}
	}
}
