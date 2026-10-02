package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/herdr"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreejournal"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// fakeHerdr is herdr's agent list: the agents it answers, the error it fails
// with, and whether it never answers (until its context ends). calls counts the
// lists, when set.
type fakeHerdr struct {
	agents []herdr.Agent
	err    error
	hang   bool
	calls  *atomic.Int64
	// entered, when set, is told when a hung call has begun, and ended when it
	// has returned.
	entered, ended chan struct{}
}

func (f fakeHerdr) AgentList(ctx context.Context) ([]herdr.Agent, error) {
	if f.calls != nil {
		f.calls.Add(1)
	}
	if f.hang {
		if f.entered != nil {
			f.entered <- struct{}{}
			defer func() { f.ended <- struct{}{} }()
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.agents, f.err
}

// herdrAgent is an agent of herdr's list, with a sentinel in every field that
// is content (its working directory, terminal title and pane).
func herdrAgent(status herdr.AgentStatus, harnessID string) herdr.Agent {
	agent := filled[herdr.Agent]()
	agent.Status = status
	agent.Session = &herdr.AgentSession{Agent: sentinel + "agent", Kind: "id", Source: sentinel + "source", Value: harnessID}
	if harnessID == "" {
		agent.Session = nil
	}
	return agent
}

func activityOver(lister HerdrLister) ActivityCollector {
	return HerdrActivity{Open: func() (HerdrLister, error) { return lister, nil }}
}

func agentsBySession(document Document) map[string]Agent {
	byID := map[string]Agent{}
	for _, agent := range document.Agents {
		byID[firstNonEmpty(agent.SessionID, agent.RunID)] = agent
	}
	return byID
}

// TestAgentActivityJoinsHerdrBySession proves
// cockpit-views#ac:agent-activity-joins-herdr-by-session: a session whose
// harness id herdr lists carries herdr's status, an unmatched session, a parked
// one and every agent with no herdr carry none, herdr is listed once per
// refresh, and no screen text, path or pane reaches the document.
func TestAgentActivityJoinsHerdrBySession(t *testing.T) {
	t.Parallel()
	view := func(id, harness, state string) session.View {
		return session.View{Record: session.Record{WBSessionID: id, NativeHarnessID: harness, Runtime: "claude", StartedAt: newClock().Now()}, State: state}
	}
	sources := oneRepoSources(t.TempDir())
	sources.sessions = []session.View{
		view("wbs-blocked", "h-1", session.StateLive), view("wbs-nomatch", "h-none", session.StateLive),
		view("wbs-parked", "h-3", session.StateParked), view("wbs-noharness", "", session.StateLive),
	}
	var calls atomic.Int64
	sources.activity = activityOver(fakeHerdr{calls: &calls, agents: []herdr.Agent{
		herdrAgent(herdr.StatusBlocked, "h-1"), herdrAgent(herdr.StatusWorking, "h-2"), herdrAgent(herdr.StatusWorking, "h-3"), herdrAgent(herdr.StatusWorking, ""),
	}})
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	refreshAndSettle(t, snapshotter)
	if calls.Load() != 2 {
		t.Errorf("herdr was listed %d times for 2 refreshes", calls.Load())
	}
	server := newCockpitServer(t, snapshotter)
	body := server.get("/api/v1/cockpit/fleet", nil).Body.String()
	byID := agentsBySession(snapshotter.Document())
	for id, want := range map[string]string{"wbs-blocked": ActivityBlocked, "wbs-nomatch": "", "wbs-parked": "", "wbs-noharness": ""} {
		if byID[id].Activity != want {
			t.Errorf("%s activity = %q, want %q", id, byID[id].Activity, want)
		}
	}
	if strings.Contains(body, sentinel) || strings.Count(body, `"activity"`) != 1 {
		t.Errorf("the document carries herdr content or the wrong activities: %s", body)
	}

	// With no herdr at all, no agent has an activity.
	sources.activity = nil
	without, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, without)
	if strings.Contains(newCockpitServer(t, without).get("/api/v1/cockpit/fleet", nil).Body.String(), `"activity"`) {
		t.Error("an agent has an activity with no herdr")
	}
}

// TestAgentActivityIsOmittedWhenHerdrFailsOrIsSlowAndDoesNotDelayTheSnapshot
// proves a herdr that errors, panics or hangs leaves the activity unreported (a
// value from an earlier refresh is dropped too), with no diagnostic, and that
// the refresh itself does not wait for a hung herdr.
func TestAgentActivityIsOmittedWhenHerdrFailsOrIsSlowAndDoesNotDelayTheSnapshot(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	sources.sessions = []session.View{{Record: session.Record{WBSessionID: "wbs-1", NativeHarnessID: "h-1"}, State: session.StateLive}}
	var logged atomic.Int64
	working := fakeHerdr{agents: []herdr.Agent{herdrAgent(herdr.StatusWorking, "h-1")}}
	current := atomic.Pointer[ActivityCollector]{}
	use := func(collector ActivityCollector) { current.Store(&collector) }
	use(activityOver(working))
	sources.activity = switching{current: &current}
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) {
		options.ActivityTimeout = 20 * time.Millisecond
		options.Logf = func(string, ...any) { logged.Add(1) }
	})
	refreshAndSettle(t, snapshotter)
	if got := agentsBySession(snapshotter.Document())["wbs-1"].Activity; got != ActivityWorking {
		t.Fatalf("activity = %q, want working (the test would be vacuous)", got)
	}
	for name, collector := range map[string]ActivityCollector{
		"an error":      activityOver(fakeHerdr{err: errors.New("no server")}),
		"a hang":        activityOver(fakeHerdr{hang: true}),
		"not installed": HerdrActivity{Open: func() (HerdrLister, error) { return nil, herdr.ErrBinaryNotFound }},
		"a panic":       panicking{},
	} {
		use(collector)
		refreshAndSettle(t, snapshotter)
		if got := agentsBySession(snapshotter.Document())["wbs-1"].Activity; got != "" {
			t.Errorf("%s: activity = %q, want none", name, got)
		}
		use(activityOver(working))
		refreshAndSettle(t, snapshotter)
		if got := agentsBySession(snapshotter.Document())["wbs-1"].Activity; got != ActivityWorking {
			t.Errorf("%s: activity did not come back (%q)", name, got)
		}
	}
	if logged.Load() != 0 {
		t.Errorf("%d diagnostics were logged for a herdr that is absent, failing or slow", logged.Load())
	}

	// The refresh returns without waiting for a hung herdr: the pass ends and the
	// document is complete while the herdr read is still waiting for its timeout.
	// herdr hangs until the context of its read ends, which here is only when the
	// test ends it: a refresh that waited for herdr would never return.
	hung := &atomic.Int64{}
	entered, ended := make(chan struct{}, 1), make(chan struct{}, 1)
	use(activityOver(fakeHerdr{hang: true, calls: hung, entered: entered, ended: ended}))
	snapshotter.activityTimeout = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	refreshed := make(chan error, 1)
	go func() { refreshed <- snapshotter.Refresh(ctx) }()
	<-entered
	select {
	case err := <-refreshed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ended:
		t.Fatal("the herdr read ended before the refresh did: the test proved nothing")
	case <-time.After(30 * time.Second):
		t.Fatal("the refresh waited for a herdr that hangs")
	}
	if snapshotter.Document().WarmingUp {
		t.Error("the pass did not complete while herdr hung")
	}
	select {
	case <-ended:
		t.Fatal("the herdr read had ended when the refresh returned: the test proved nothing")
	default:
	}
	cancel()
	<-ended
	snapshotter.side.Wait()
	if hung.Load() != 1 {
		t.Errorf("hung herdr was listed %d times", hung.Load())
	}
}

// switching is an ActivityCollector whose answer a test changes between refreshes.
type switching struct {
	current *atomic.Pointer[ActivityCollector]
}

func (s switching) Activity(ctx context.Context) (map[string]string, error) {
	return (*s.current.Load()).Activity(ctx)
}

type panicking struct{}

func (panicking) Activity(context.Context) (map[string]string, error) { panic("herdr exploded") }

// TestHerdrActivityMapsStatusesAndNeverGuesses covers the closed vocabulary and
// the join rules: five statuses, an unknown word reads unknown, an agent without
// a harness session id is left out, and an id two agents disagree on is left out.
func TestHerdrActivityMapsStatusesAndNeverGuesses(t *testing.T) {
	t.Parallel()
	got, err := activityOver(fakeHerdr{agents: []herdr.Agent{
		herdrAgent(herdr.StatusWorking, "w"), herdrAgent(herdr.StatusBlocked, "b"), herdrAgent(herdr.StatusIdle, "i"), herdrAgent(herdr.StatusDone, "d"),
		herdrAgent(herdr.StatusUnknown, "u"), herdrAgent("exploding", "x"), herdrAgent(herdr.StatusIdle, ""),
		herdrAgent(herdr.StatusWorking, "same"), herdrAgent(herdr.StatusWorking, "same"),
		herdrAgent(herdr.StatusWorking, "clash"), herdrAgent(herdr.StatusIdle, "clash"), herdrAgent(herdr.StatusIdle, "clash"),
	}}).Activity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"w": "working", "b": "blocked", "i": "idle", "d": "done", "u": "unknown", "x": "unknown", "same": "working"}
	if len(got) != len(want) {
		t.Fatalf("activity = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("activity[%s] = %q, want %q", key, got[key], value)
		}
	}
	if _, err := (HerdrActivity{Open: func() (HerdrLister, error) { return nil, herdr.ErrBinaryNotFound }}).Activity(t.Context()); !errors.Is(err, herdr.ErrBinaryNotFound) {
		t.Errorf("a herdr that is not installed = %v", err)
	}
}

// TestHerdrActivityOpensTheResolvedBinaryWithoutRunningIt covers the production
// Open: with no herdr it fails, and with a binary named by HERDR_BIN_PATH it
// returns a client without running the binary.
func TestHerdrActivityOpensTheResolvedBinaryWithoutRunningIt(t *testing.T) {
	t.Parallel()
	binary := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(binary, []byte("not run"), 0o644); err != nil {
		t.Fatal(err)
	}
	named := herdrActivityIn(func(key string) (string, bool) { return binary, key == "HERDR_BIN_PATH" })
	if lister, err := named.Open(); err != nil || lister == nil {
		t.Fatalf("open = %v, %v", lister, err)
	}
	if lister, err := herdrActivityIn(nil).Open(); err == nil || lister != nil {
		t.Errorf("open with no environment = %v, %v", lister, err)
	}
	if DefaultHerdrActivity().Open == nil {
		t.Error("the default has no opener")
	}
}

// TestAgentEntriesCarryRunLinks proves
// cockpit-views#ac:agent-entries-carry-run-links: a running run and a finished
// run carry their repository, task, worktree and start time (the finished one
// also its finish time and exit code, never its free-text failure), a live
// session that a worktree's owner process names carries that worktree and task,
// a session nothing names has only its start time, and a parked session never
// borrows a worktree.
func TestAgentEntriesCarryRunLinks(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	now := newClock().Now()
	taskA := sources.records["/wt/task-a"]
	taskA.OwnerPID = 4242
	sources.records["/wt/task-a"] = taskA
	finished := now.Add(-time.Hour)
	two, negative := 2, -1
	sources.runs = []agents.Result{
		{AgentID: "agt-run", State: agents.StateRunning, Repository: "acme/widgets", Worktree: "task-b", Branch: "feature/b", StartedAt: now.Add(-time.Minute), ExitCode: &two, FinishedAt: &finished, Failure: sentinel + "failure"},
		{AgentID: "agt-failed", State: agents.StateFailed, Repository: "acme/widgets", Worktree: "task-a", Branch: "feature/a", StartedAt: now.Add(-2 * time.Hour), FinishedAt: &finished, ExitCode: &two, Failure: sentinel + "failure"},
		{AgentID: "agt-signalled", State: agents.StateFailed, Repository: "acme/widgets", Worktree: "task-b", StartedAt: now.Add(-2 * time.Hour), FinishedAt: &finished, ExitCode: &negative},
		{AgentID: "agt-elsewhere", State: agents.StateRunning, Repository: "acme/other", Worktree: "task-z", Branch: "feature/z", StartedAt: now},
	}
	started := now.Add(-3 * time.Hour)
	live := func(id string, pid int) session.View {
		return session.View{Record: session.Record{WBSessionID: id, PID: pid, StartedAt: started}, State: session.StateLive}
	}
	sources.sessions = []session.View{
		live("wbs-claimed", 4242), live("wbs-bare", 4343), {Record: session.Record{WBSessionID: "wbs-parked", PID: 4242, StartedAt: started}, State: session.StateParked},
	}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	document := snapshotter.Document()
	byID := agentsBySession(document)
	ids := map[string]string{}
	for _, worktree := range document.Worktrees {
		ids[worktree.Task] = worktree.ID
	}
	repository := ""
	for _, candidate := range document.Repositories {
		if candidate.Route == RouteLocal {
			repository = candidate.ID
		}
	}
	running, failed := byID["agt-run"], byID["agt-failed"]
	if running.Repository != repository || running.Task != "task-b" || len(running.Worktrees) != 1 || running.Worktrees[0] != ids["task-b"] || !running.StartedAt.Equal(now.Add(-time.Minute)) {
		t.Errorf("running run = %+v", running)
	}
	if !running.FinishedAt.IsZero() || running.ExitCode != nil {
		t.Errorf("a running run has a finish time or exit code: %+v", running)
	}
	if failed.Task != "task-a" || failed.Worktrees[0] != ids["task-a"] || !failed.FinishedAt.Equal(finished) || failed.ExitCode == nil || *failed.ExitCode != 2 || failed.State != "failed" {
		t.Errorf("failed run = %+v", failed)
	}
	if signalled := byID["agt-signalled"]; signalled.ExitCode != nil || signalled.Task != "task-b" || len(signalled.Worktrees) != 1 {
		t.Errorf("a run with no branch and a negative exit code = %+v (matched by task name; no exit code)", signalled)
	}
	if elsewhere := byID["agt-elsewhere"]; elsewhere.Repository != "" || len(elsewhere.Worktrees) != 0 || elsewhere.Task != "task-z" {
		t.Errorf("a run in a repository this machine does not hold = %+v", elsewhere)
	}
	claimed, bare, parked := byID["wbs-claimed"], byID["wbs-bare"], byID["wbs-parked"]
	if claimed.Task != "task-a" || claimed.Repository != repository || len(claimed.Worktrees) != 1 || claimed.Worktrees[0] != ids["task-a"] || !claimed.StartedAt.Equal(started) || claimed.State != "live" {
		t.Errorf("claimed session = %+v", claimed)
	}
	if bare.Task != "" || bare.Repository != "" || len(bare.Worktrees) != 0 || !bare.StartedAt.Equal(started) {
		t.Errorf("bare session = %+v", bare)
	}
	if parked.Task != "" || len(parked.Worktrees) != 0 || parked.State != "parked" || !parked.StartedAt.Equal(started) {
		t.Errorf("parked session = %+v", parked)
	}
	if body := newCockpitServer(t, snapshotter).get("/api/v1/cockpit/fleet", nil).Body.String(); strings.Contains(body, sentinel) || strings.Contains(body, "failure") {
		t.Errorf("the document carries a run's free-text failure: %s", body)
	}
}

// TestSessionWithSeveralWorktreesNamesNoTaskOrRepositoryItCannotAgreeOn covers
// the ambiguous link: two worktrees of different tasks held by one process list
// both worktrees but name no task, and more than the bound are cut.
func TestSessionWithSeveralWorktreesNamesNoTaskOrRepositoryItCannotAgreeOn(t *testing.T) {
	t.Parallel()
	owners := map[int][]ownerLink{7: {
		{pid: 7, worktree: "wt-b", task: "one", project: "r1"}, {pid: 7, worktree: "wt-a", task: "two", project: "r2"}, {pid: 7, worktree: "wt-a", task: "two", project: "r2"},
	}}
	record := agentRecord{agent: Agent{Kind: AgentSession, State: session.StateLive}, pid: 7}
	agent := completeAgent(record, "", nil, owners, nil)
	if agent.Task != "" || agent.Repository != "" || len(agent.Worktrees) != 2 || agent.Worktrees[0] != "wt-a" {
		t.Errorf("agent = %+v", agent)
	}
	var many []ownerLink
	for index := range maxAgentWorktrees + 5 {
		many = append(many, ownerLink{pid: 8, worktree: string(rune('a' + index)), task: "t", project: "r"})
	}
	record.pid = 8
	if agent := completeAgent(record, "", nil, map[int][]ownerLink{8: many}, nil); len(agent.Worktrees) != maxAgentWorktrees || agent.Task != "t" || agent.Repository != "r" {
		t.Errorf("agent = %+v", agent)
	}
}

// TestRecordLinksALiveOwnerAndRefusesAReusedProcessId covers the owner link of a
// worktree record: the live owner's process id, declared agent and observed
// start are kept, a process observed to have started after the owner registered
// is a reused id and is dropped, and a platform that cannot observe a start
// keeps the live check alone.
func TestRecordLinksALiveOwnerAndRefusesAReusedProcessId(t *testing.T) {
	t.Parallel()
	dir := realTempDir(t)
	writeManifestFile(t, dir, worktrees.Manifest{
		Version: 1, EffortID: "task-q", EffortKind: worktrees.EffortKindTask, Provenance: worktrees.ProvenanceCreated,
		Repository: "acme/widgets", Branch: "task-q", CreatedAt: time.Now().UTC(),
	})
	registered := time.Now().UTC()
	content, err := worktreejournal.Store{}.EncodeLocalEvents([]worktrees.LocalWorkLogEvent{{
		Version: 1, Seq: 0, ID: "evt-a", Type: worktrees.LocalEventOwner, At: registered,
		Owner: &worktrees.OwnerRegistration{Agent: "claude/hid-1", PID: os.Getpid(), At: registered},
	}})
	if err != nil {
		t.Fatal(err)
	}
	worklog := filepath.Join(dir, ".wb", "local", "worklog")
	if err := os.MkdirAll(worklog, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worklog, "events.jsonl"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	at := func(offset time.Duration) func(int) (time.Time, bool) {
		return func(pid int) (time.Time, bool) { return registered.Add(offset), pid == os.Getpid() }
	}
	for name, test := range map[string]struct {
		observe   func(int) (time.Time, bool)
		wantPID   int
		wantStart bool
	}{
		"started before the owner registered": {at(-time.Hour), os.Getpid(), true},
		"started after the owner registered":  {at(time.Hour), 0, false},
		"start not observable":                {func(int) (time.Time, bool) { return time.Time{}, false }, os.Getpid(), false},
	} {
		record, ok := LocalCollectors{ProcessStart: test.observe}.Record(dir)
		if !ok || record.Owner != worktrees.OwnerLive || record.OwnerPID != test.wantPID || record.OwnerStarted.IsZero() == test.wantStart {
			t.Errorf("%s: record = %+v", name, record)
		}
		if test.wantPID != 0 && record.OwnerAgent != "claude/hid-1" {
			t.Errorf("%s: owner agent = %q", name, record.OwnerAgent)
		}
	}
	// With no seam the real observation answers, and never fails the record.
	if record, ok := (LocalCollectors{}).Record(dir); !ok || record.Owner != worktrees.OwnerLive {
		t.Errorf("record with the real process observation = %+v", record)
	}
}

// TestSessionAndOwnerMustAgreeBesideTheProcessId covers the reuse guard of the
// session link: a reused process id is refused when the declared runtime or
// harness id disagrees or the owner's process started after the session
// registered, and the link stands when the records agree or carry nothing
// to compare.
func TestSessionAndOwnerMustAgreeBesideTheProcessId(t *testing.T) {
	t.Parallel()
	registered := newClock().Now()
	live := func(runtime string) Agent {
		return Agent{Kind: AgentSession, State: session.StateLive, Runtime: runtime, StartedAt: registered}
	}
	for name, test := range map[string]struct {
		agent   Agent
		harness string
		link    ownerLink
		want    bool
	}{
		"same runtime and id":              {live("claude"), "h", ownerLink{agent: "claude/h"}, true},
		"another runtime":                  {live("codex"), "h", ownerLink{agent: "claude/h"}, false},
		"another harness id":               {live("claude"), "h2", ownerLink{agent: "claude/h"}, false},
		"a record with no runtime":         {live(""), "h", ownerLink{agent: "claude/h"}, true},
		"an owner with a bare runtime":     {live("claude"), "", ownerLink{agent: "claude"}, true},
		"an owner with a bare harness id":  {live(""), "h", ownerLink{agent: "h"}, true},
		"an owner with another bare token": {live("claude"), "h", ownerLink{agent: "codex"}, false},
		"an owner that declared no agent":  {live("claude"), "h", ownerLink{}, true},
		"a session that declared nothing":  {live(""), "", ownerLink{agent: "codex"}, true},
		"owner process older than session": {live("claude"), "", ownerLink{started: registered.Add(-time.Hour)}, true},
		"owner process newer than session": {live("claude"), "", ownerLink{started: registered.Add(time.Hour)}, false},
		"start unknown":                    {live("claude"), "", ownerLink{}, true},
		"session with no start time":       {Agent{Kind: AgentSession}, "", ownerLink{started: registered}, true},
	} {
		if got := sameProcess(test.agent, test.harness, test.link); got != test.want {
			t.Errorf("%s: sameProcess = %v, want %v", name, got, test.want)
		}
	}
	// And through the document: a session whose process id the owner shares but
	// whose runtime differs carries no worktree.
	record := agentRecord{agent: live("codex"), pid: 7, harness: "h"}
	owners := map[int][]ownerLink{7: {{pid: 7, agent: "claude/h", worktree: "wt-a", task: "t", project: "r"}}}
	if agent := completeAgent(record, "", nil, owners, nil); len(agent.Worktrees) != 0 || agent.Task != "" {
		t.Errorf("a reused process id linked a worktree: %+v", agent)
	}
}
