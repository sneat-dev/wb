package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestRefreshSkipsGitWorkButReadsRecordsForAnUnchangedFingerprint proves
// cockpit#req:snapshot-refresh: a repository whose fingerprint did not move
// costs no worktree or branch read however many passes run, a moved one costs
// one, and its worktrees' heartbeats are read on every pass, so an owner state
// that changes outside Git still shows.
func TestRefreshSkipsGitWorkButReadsRecordsForAnUnchangedFingerprint(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	fingerprint := newConstFingerprint("one")
	snapshotter, clock := newSnapshotter(sources.collectors(), func(options *Options) { options.Fingerprint = fingerprint.get })
	refresh := func() {
		t.Helper()
		if err := snapshotter.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	refresh()
	for range 10 {
		refresh()
	}
	if sources.worktreeCalls.Load() != 1 {
		t.Fatalf("11 passes with an unchanged fingerprint read the Git state %d times", sources.worktreeCalls.Load())
	}
	if fingerprint.calls.Load() != 11 {
		t.Errorf("the fingerprint was computed %d times over 11 passes of one repository, want once per pass", fingerprint.calls.Load())
	}
	if sources.recordCalls.Load() != 22 {
		t.Errorf("records were read %d times over 11 passes of 2 worktrees, want every pass", sources.recordCalls.Load())
	}
	ownerOf := func(task string) string {
		for _, worktree := range localWorktrees(snapshotter.Document()) {
			if worktree.Task == task {
				return worktree.OwnerState
			}
		}
		return ""
	}
	if ownerOf("task-a") != OwnerActive || ownerOf("task-b") != OwnerIdle {
		t.Fatalf("owner states = %q, %q", ownerOf("task-a"), ownerOf("task-b"))
	}
	clock.advance(7 * time.Hour)
	refresh()
	if ownerOf("task-a") != OwnerIdle || sources.worktreeCalls.Load() != 1 {
		t.Errorf("task-a is %q after its heartbeat aged, with %d Git reads", ownerOf("task-a"), sources.worktreeCalls.Load())
	}
	fingerprint.value.Store("two")
	refresh()
	if sources.worktreeCalls.Load() != 2 {
		t.Errorf("a changed fingerprint was read %d times in total, want 2", sources.worktreeCalls.Load())
	}
}

// TestFailingRepositoryIsRecordedWithACodeAndKeepsItsLastState requires a
// failing read to show an error code, never a path or message, and to keep the
// last good worktrees and branches; a failing source keeps its last value.
func TestFailingRepositoryIsRecordedWithACodeAndKeepsItsLastState(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Fingerprint = func(string) (string, error) { return "", errBoom }
	})
	refreshAndSettle(t, snapshotter)
	before := snapshotter.Document()

	sources.change(func(f *fakeSources) {
		f.worktreeErr, f.sessionErr, f.remoteErr, f.bindingErr = errBoom, errBoom, errBoom, errBoom
		f.sessions, f.remote = nil, nil
	})
	err := snapshotter.Refresh(t.Context())
	snapshotter.side.Wait()
	for _, want := range []string{"acme/widgets", ErrorReadFailed, "read agents", "read pull request records"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("refresh error = %v, want it to name %q", err, want)
		}
	}
	after := snapshotter.Document()
	var local Repository
	for _, repository := range after.Repositories {
		if repository.Route == RouteLocal {
			local = repository
		}
	}
	if len(snapshotter.Document().Machines) != 2 {
		t.Errorf("a failing remote read dropped the other machine: %+v", snapshotter.Document().Machines)
	}
	if local.Error != ErrorReadFailed || local.WorktreeCount != 2 || len(after.Worktrees) != len(before.Worktrees) || len(after.Branches) != len(before.Branches) || len(after.Agents) != 1 || len(after.Machines) != 2 {
		t.Errorf("failed sources dropped state: repository %+v, %d worktrees, %d agents, %d machines", local, len(after.Worktrees), len(after.Agents), len(after.Machines))
	}
	body, _ := jsonString(after)
	if strings.Contains(body, "boom") {
		t.Errorf("an error message reached the document: %s", body)
	}

	sources.change(func(f *fakeSources) { f.worktreeErr, f.branchErr, f.runErr = nil, errBoom, errBoom })
	if err := snapshotter.Refresh(t.Context()); err == nil || !strings.Contains(err.Error(), "read agents") || !strings.Contains(err.Error(), ErrorReadFailed) {
		t.Errorf("run and branch failures = %v", err)
	}
	sources.change(func(f *fakeSources) { f.branchErr = nil })
	if err := snapshotter.Refresh(t.Context()); err == nil {
		t.Error("a pass with a failing source reported nothing")
	}
	for _, repository := range snapshotter.Document().Repositories {
		if repository.Route == RouteLocal && repository.Error != "" {
			t.Errorf("a repository that read cleanly still carries %q", repository.Error)
		}
	}
}

// slowWorktrees blocks until its context ends.
type slowWorktrees struct{}

func (slowWorktrees) Worktrees(ctx context.Context, _ discover.Repo) ([]LinkedWorktree, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestSlowRepositoryTimesOutWithoutStallingThePass proves the per-repository
// timeout: the slow repository is recorded with the timeout code and the
// others complete.
func TestSlowRepositoryTimesOutWithoutStallingThePass(t *testing.T) {
	t.Parallel()
	sources := &fakeSources{repos: []discover.Repo{
		{Org: "acme", Name: "slow", Path: t.TempDir()}, {Org: "acme", Name: "fast", Path: t.TempDir()},
	}}
	collectors := sources.collectors()
	collectors.Worktrees = onlyFor{slug: "acme/slow", slow: slowWorktrees{}, otherwise: sources}
	snapshotter, _ := newSnapshotter(collectors, func(options *Options) { options.RepositoryTimeout = 20 * time.Millisecond })
	err := snapshotter.Refresh(t.Context())
	if err == nil || !strings.Contains(err.Error(), "acme/slow: "+ErrorTimeout) {
		t.Fatalf("refresh = %v, want the slow repository's timeout", err)
	}
	codes := map[string]string{}
	for _, repository := range snapshotter.Document().Repositories {
		codes[repository.Name] = repository.Error
	}
	if len(codes) != 2 || codes["acme/slow"] != ErrorTimeout || codes["acme/fast"] != "" || snapshotter.Document().WarmingUp {
		t.Errorf("repositories = %v, warming %v", codes, snapshotter.Document().WarmingUp)
	}
}

// onlyFor routes one repository's worktree read to slow and the rest to
// otherwise.
type onlyFor struct {
	slug      string
	slow      WorktreeCollector
	otherwise WorktreeCollector
}

func (o onlyFor) Worktrees(ctx context.Context, repo discover.Repo) ([]LinkedWorktree, error) {
	if repo.Slug() == o.slug {
		return o.slow.Worktrees(ctx, repo)
	}
	return o.otherwise.Worktrees(ctx, repo)
}

// TestRefreshFailsWhenTheRepositoriesCannotBeListed keeps the document warming
// when there has never been a scan.
func TestRefreshFailsWhenTheRepositoriesCannotBeListed(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	sources.repoErr = errBoom
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	if err := snapshotter.Refresh(t.Context()); !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "list repositories") {
		t.Fatalf("refresh = %v", err)
	}
	if !snapshotter.Document().WarmingUp {
		t.Error("a failed first scan ended the warm-up")
	}
}

// TestCancelledRefreshDoesNotEndTheWarmUp requires a refresh whose context ends
// to stop starting work and not to complete the first pass.
func TestCancelledRefreshDoesNotEndTheWarmUp(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := snapshotter.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh = %v, want context.Canceled", err)
	}
	if !snapshotter.Document().WarmingUp || sources.worktreeCalls.Load() != 0 {
		t.Errorf("a cancelled refresh completed the warm-up or read a repository (%d reads)", sources.worktreeCalls.Load())
	}
	// A repository read cut short by cancellation is not recorded either.
	snapshotter.track(sources.repos)
	if err := snapshotter.scanRepository(ctx, localRepositoryID(testMachine, sources.repos[0]), false); !errors.Is(err, context.Canceled) {
		t.Errorf("scan under a cancelled context = %v", err)
	}
	if snapshotter.Document().RepositoriesScanned != 0 {
		t.Error("a cancelled repository read was recorded as scanned")
	}
}

// gatedWorktrees lets a test hold one repository's read open.
type gatedWorktrees struct {
	inner   WorktreeCollector
	slug    string
	reached chan struct{}
	release chan struct{}
}

func (g gatedWorktrees) Worktrees(ctx context.Context, repo discover.Repo) ([]LinkedWorktree, error) {
	if repo.Slug() == g.slug {
		close(g.reached)
		<-g.release
	}
	return g.inner.Worktrees(ctx, repo)
}

// TestDocumentIsReadableWhileTheFirstPassRuns proves incremental publication:
// with the second repository held open, the document already lists the first,
// says 1 of 2 scanned and stays warming, and completes when released.
func TestDocumentIsReadableWhileTheFirstPassRuns(t *testing.T) {
	t.Parallel()
	sources := &fakeSources{repos: []discover.Repo{
		{Org: "acme", Name: "a", Path: t.TempDir()}, {Org: "acme", Name: "b", Path: t.TempDir()},
	}}
	gate := gatedWorktrees{inner: sources, slug: "acme/b", reached: make(chan struct{}), release: make(chan struct{})}
	collectors := sources.collectors()
	collectors.Remote = nil
	collectors.Worktrees = gate
	snapshotter, _ := newSnapshotter(collectors, func(options *Options) { options.Workers = 2 })
	done := make(chan error, 1)
	go func() { done <- snapshotter.Refresh(t.Context()) }()
	<-gate.reached
	waitFor(t, "the first repository to be published", func() bool { return snapshotter.Document().RepositoriesScanned == 1 })
	partial := snapshotter.Document()
	if !partial.WarmingUp || partial.RepositoriesTotal != 2 || len(partial.Repositories) != 2 && len(partial.Repositories) != 1 {
		t.Errorf("partial document = warming %v, %d of %d scanned, %d repositories", partial.WarmingUp, partial.RepositoriesScanned, partial.RepositoriesTotal, len(partial.Repositories))
	}
	close(gate.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if full := snapshotter.Document(); full.WarmingUp || full.RepositoriesScanned != 2 || full.RepositoriesTotal != 2 {
		t.Errorf("final document = warming %v, %d of %d scanned", full.WarmingUp, full.RepositoriesScanned, full.RepositoriesTotal)
	}
}

// TestRefreshRepositoryRereadsOneRepositoryOnRequest proves the on-request half
// of cockpit#req:snapshot-refresh: a completed operation shows without waiting
// for the interval, even when the fingerprint did not move; an unknown id is
// refused; and a failing read is returned and shown as an error code.
func TestRefreshRepositoryRereadsOneRepositoryOnRequest(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	fingerprint := newConstFingerprint("same")
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) { options.Fingerprint = fingerprint.get })

	if err := snapshotter.RefreshRepository(t.Context(), "repo-unknown"); !errors.Is(err, ErrUnknownRepository) {
		t.Fatalf("refresh of an unknown repository = %v", err)
	}
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	id := localRepositoryID(testMachine, sources.repos[0])
	sources.change(func(f *fakeSources) {
		f.worktrees["acme/widgets"] = append(f.worktrees["acme/widgets"], LinkedWorktree{Path: "/wt/task-c", Branch: "feature/c"})
		f.records["/wt/task-c"] = WorktreeRecord{Task: "task-c", Branch: "feature/c"}
	})
	if len(localWorktrees(snapshotter.Document())) != 2 {
		t.Fatal("the new worktree appeared before any refresh")
	}
	if err := snapshotter.RefreshRepository(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if got := len(localWorktrees(snapshotter.Document())); got != 3 {
		t.Errorf("after RefreshRepository the document lists %d local worktrees, want 3", got)
	}
	sources.change(func(f *fakeSources) { f.branchErr = errBoom })
	if err := snapshotter.RefreshRepository(t.Context(), id); err == nil {
		t.Error("a failing read did not return its error")
	}
	for _, repository := range snapshotter.Document().Repositories {
		if repository.Route == RouteLocal && repository.Error != ErrorReadFailed {
			t.Errorf("republished repository = %+v, want the error code", repository)
		}
	}
}

// TestRefreshRepositoryBeforeThePassStaysWarming keeps a read of one
// repository from ending the warm-up.
func TestRefreshRepositoryBeforeThePassStaysWarming(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	snapshotter.track(sources.repos)
	if err := snapshotter.RefreshRepository(t.Context(), localRepositoryID(testMachine, sources.repos[0])); err != nil {
		t.Fatal(err)
	}
	if document := snapshotter.Document(); !document.WarmingUp || document.RepositoriesScanned != 1 {
		t.Errorf("document = warming %v, %d scanned", document.WarmingUp, document.RepositoriesScanned)
	}
}

// TestRefreshForgetsRepositoriesThatDisappearAndSkipsRemoteOnly also checks
// that a repository with no local clone is not listed.
func TestRefreshForgetsRepositoriesThatDisappearAndSkipsRemoteOnly(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	sources.repos = append(sources.repos, discover.Repo{Org: "acme", Name: "remote-only"})
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	id := localRepositoryID(testMachine, sources.repos[0])
	if _, found := snapshotter.Checkout(id); !found {
		t.Fatal("the local repository is not tracked")
	}
	if document := snapshotter.Document(); strings.Contains(repositoryNames(document), "remote-only") || document.RepositoriesTotal != 1 {
		t.Errorf("a repository with no local clone was counted: %s, total %d", repositoryNames(document), document.RepositoriesTotal)
	}
	sources.change(func(f *fakeSources) { f.repos = nil })
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, found := snapshotter.Checkout(id); found {
		t.Error("a repository that left the projects root is still tracked")
	}
	if got := len(localWorktrees(snapshotter.Document())); got != 0 {
		t.Errorf("%d worktrees of a vanished repository remain", got)
	}
}

func repositoryNames(document Document) string {
	var names []string
	for _, repository := range document.Repositories {
		names = append(names, repository.Name)
	}
	return strings.Join(names, ",")
}

// countingWorktrees records how many reads overlap.
type countingWorktrees struct {
	mu            sync.Mutex
	running, peak int
	read          int
}

func (c *countingWorktrees) Worktrees(context.Context, discover.Repo) ([]LinkedWorktree, error) {
	c.mu.Lock()
	c.running++
	c.peak = max(c.peak, c.running)
	c.read++
	c.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	c.mu.Lock()
	c.running--
	c.mu.Unlock()
	return nil, nil
}

// TestRepositoriesAreReadWithBoundedParallelism requires no more than the
// configured workers to read at once, and every repository to be read.
func TestRepositoriesAreReadWithBoundedParallelism(t *testing.T) {
	t.Parallel()
	const repositories, workers = 12, 3
	counter := &countingWorktrees{}
	sources := &fakeSources{}
	for index := range repositories {
		sources.repos = append(sources.repos, discover.Repo{Org: "acme", Name: "repo" + string(rune('a'+index)), Path: t.TempDir()})
	}
	collectors := sources.collectors()
	collectors.Worktrees = counter
	snapshotter, _ := newSnapshotter(collectors, func(options *Options) { options.Workers = workers })
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if counter.read != repositories || counter.peak > workers || counter.peak < 2 {
		t.Errorf("read %d of %d repositories with a peak of %d at once, want at most %d", counter.read, repositories, counter.peak, workers)
	}
}

// TestStartRefreshesOnEveryTickAndStopsCleanly drives the loop with a manual
// tick source and requires stop to end it with no goroutine left.
func TestStartRefreshesOnEveryTickAndStopsCleanly(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	ticks := make(chan time.Time)
	var requested time.Duration
	var stopped sync.WaitGroup
	stopped.Add(1)
	var logged []string
	var logMu sync.Mutex
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Interval = 42 * time.Second
		options.Tick = func(interval time.Duration) (<-chan time.Time, func()) {
			requested = interval
			return ticks, stopped.Done
		}
		options.Logf = func(format string, args ...any) {
			logMu.Lock()
			defer logMu.Unlock()
			logged = append(logged, format)
		}
	})
	sources.change(func(f *fakeSources) { f.worktreeErr = errBoom })
	stop := snapshotter.Start(t.Context())
	waitFor(t, "the first refresh", func() bool { return !snapshotter.Document().WarmingUp })
	ticks <- time.Time{}
	waitFor(t, "a second refresh", func() bool { return sources.worktreeCalls.Load() >= 2 })
	stop()
	stopped.Wait()
	if requested != 42*time.Second {
		t.Errorf("ticks were requested for %v, want 42s", requested)
	}
	logMu.Lock()
	reported := append([]string(nil), logged...)
	logMu.Unlock()
	if len(reported) == 0 || !strings.Contains(reported[0], "cockpit fleet refresh") {
		t.Errorf("a failed refresh was not reported: %v", reported)
	}
}

// TestStopDuringTheFirstRefreshReportsNothingAndReturns requires a cancelled
// refresh not to be logged as a failure.
func TestStopDuringTheFirstRefreshReportsNothingAndReturns(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	collectors := oneRepoSources(t.TempDir()).collectors()
	collectors.Repositories = blockingRepositories{started: started, release: release}
	logged := make(chan string, 1)
	snapshotter, _ := newSnapshotter(collectors, func(options *Options) {
		options.Logf = func(format string, _ ...any) { logged <- format }
	})
	stop := snapshotter.Start(t.Context())
	<-started
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	close(release)
	<-done
	select {
	case message := <-logged:
		t.Errorf("a cancelled refresh was logged: %s", message)
	default:
	}
}

type blockingRepositories struct{ started, release chan struct{} }

func (b blockingRepositories) Repositories(ctx context.Context) ([]discover.Repo, error) {
	close(b.started)
	<-b.release
	return nil, ctx.Err()
}

// TestNewDefaultsTheIntervalWorkersTimeoutClockAndTicker pins the defaults.
func TestNewDefaultsTheIntervalWorkersTimeoutClockAndTicker(t *testing.T) {
	t.Parallel()
	snapshotter := New(Options{})
	if snapshotter.interval != DefaultInterval || snapshotter.workers != defaultWorkers() || snapshotter.repositoryTimeout != defaultRepositoryTimeout || snapshotter.stopWait != defaultStopWait {
		t.Errorf("defaults = %v, %d, %v, %v", snapshotter.interval, snapshotter.workers, snapshotter.repositoryTimeout, snapshotter.stopWait)
	}
	if workers := defaultWorkers(); workers < 1 || workers > maxWorkers {
		t.Errorf("default workers = %d", workers)
	}
	if time.Since(snapshotter.now()) > time.Minute {
		t.Error("the default clock is not the wall clock")
	}
	snapshotter.logf("discarded %d", 1)
	ticks, stop := snapshotter.tick(time.Hour)
	t.Cleanup(stop)
	select {
	case <-ticks:
		t.Error("an hourly ticker ticked at once")
	default:
	}
	if _, err := snapshotter.fingerprint(t.TempDir()); err == nil {
		t.Error("the default fingerprint accepted a directory that is not a clone")
	}
}

// TestRunsAndSessionsBecomeAgentsWithRepositoryCounts maps both kinds of agent
// and counts only the running runs of a repository as its active agents.
func TestRunsAndSessionsBecomeAgentsWithRepositoryCounts(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	sources.sessions = append(sources.sessions,
		session.View{Record: session.Record{PID: 4242}, State: session.StateLive},
		session.View{Record: session.Record{WBSessionID: "wbs-gone"}, State: session.StateGone},
		session.View{Record: session.Record{WBSessionID: "wbs-parked"}, State: session.StateParked})
	run := func(id string, state agents.State, repository string) agents.Result {
		return agents.Result{AgentID: id, State: state, Repository: repository, Resolved: agents.Resolved{Harness: "codex", Model: "gpt"}}
	}
	sources.runs = []agents.Result{
		run("agt-1", agents.StateRunning, "acme/widgets"), run("agt-2", agents.StateCompleted, "acme/widgets"),
		run("agt-3", agents.StateRunning, "elsewhere/unknown"), run("agt-1", agents.StateRunning, "acme/widgets"),
	}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	document := snapshotter.Document()
	if len(document.Agents) != 5 {
		t.Fatalf("agents = %+v, want 3 live or parked sessions and the 2 running runs", document.Agents)
	}
	var widgets Repository
	for _, repository := range document.Repositories {
		if repository.Name == "acme/widgets" && repository.Route == RouteLocal {
			widgets = repository
		}
	}
	linked, anonymous := 0, 0
	for _, agent := range document.Agents {
		if agent.Repository == widgets.ID {
			linked++
		}
		if agent.Kind == AgentSession && agent.SessionID == "" && agent.ID != "" {
			anonymous++
		}
	}
	if *widgets.ActiveAgentCount != 1 || linked != 1 || anonymous != 1 {
		t.Errorf("active agents = %d, runs linked to the repository = %d, sessions with no id = %d", *widgets.ActiveAgentCount, linked, anonymous)
	}
	if body, _ := jsonString(document); strings.Contains(body, "4242") || strings.Contains(body, "wbs-gone") {
		t.Errorf("a process id or a gone session reached the document: %s", body)
	}
}

// TestRemoteMachinesAreCachedAndSkipOurOwnPublication covers the remote
// mapping: this machine's own last publication is not repeated as cached, an
// undecodable snapshot is skipped, a merged pull request is not
// open evidence, and a worktree repeated in a snapshot has one id.
func TestRemoteMachinesAreCachedAndSkipOurOwnPublication(t *testing.T) {
	t.Parallel()
	published := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	view := mapRemote(testMachine, "", "/projects", []remotestate.Entry{
		{Snapshot: remotestate.Snapshot{Login: "me", Machine: testMachine, PublishedAt: published, ProjectsRoot: "/projects"}},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "broken", PublishedAt: published}, Error: "bad yaml"},
		{Snapshot: remotestate.Snapshot{
			Login: "a", Machine: "desk", PublishedAt: published, WBVersion: "v1",
			Worktrees: []remotestate.WorktreeState{
				{Task: "t", Repository: "o/r", Branch: "b", PullRequest: &remotestate.PullRequestState{Number: 1, State: "MERGED"}},
				{Task: "t", Repository: "o/r", Branch: "b", PullRequest: &remotestate.PullRequestState{Number: 2, State: "open"}},
				{Task: "u", Repository: "o/r", Branch: "c"},
			},
		}},
	})
	if len(view.machines) != 1 || len(view.worktrees) != 2 || len(view.pullRequests) != 1 || len(view.repositories) != 1 {
		t.Fatalf("view = %d machines, %d worktrees, %d PRs, %d repositories", len(view.machines), len(view.worktrees), len(view.pullRequests), len(view.repositories))
	}
	if view.repositories[0].WorktreeCount != 2 || *view.repositories[0].OpenPullRequestCount != 1 || view.repositories[0].LocalBranchCount != nil {
		t.Errorf("cached repository = %+v", view.repositories[0])
	}
	for _, machine := range view.machines {
		if machine.Route != RouteCached || !machine.ObservedAt.Equal(published) || machine.Machine == testMachine {
			t.Errorf("machine %+v", machine)
		}
	}
}

// TestLocalEntriesMapRecordsBranchesAndDropDuplicates covers the owner state
// from the heartbeat (falling back to creation), a detached worktree taking
// its branch from the manifest, the tracking fields, the worktree link and
// duplicate identities. No lifecycle or stream is derived.
func TestLocalEntriesMapRecordsBranchesAndDropDuplicates(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	repo := discover.Repo{Host: "github.com", Org: "o", Name: "r", Path: "/p"}
	recorded := []recordedWorktree{
		{linked: LinkedWorktree{Path: "/a", Branch: "a"}, record: WorktreeRecord{Task: "fresh", CreatedAt: at.Add(-time.Hour)}},
		{linked: LinkedWorktree{Path: "/b"}, record: WorktreeRecord{Task: "detached", Branch: "b", CreatedAt: at.Add(-30 * time.Hour), HeartbeatAt: at.Add(-time.Minute)}},
		{linked: LinkedWorktree{Path: "/c", Branch: "c"}, record: WorktreeRecord{Task: "old", CreatedAt: at.Add(-30 * time.Hour)}},
		{linked: LinkedWorktree{Path: "/c2", Branch: "c"}, record: WorktreeRecord{Task: "old", CreatedAt: at.Add(-30 * time.Hour)}},
	}
	mapped := mapLocalRepository(testMachine, repo, "trunk", recorded, []BranchRef{
		{Name: "a", Scope: BranchLocal, Upstream: "origin/a", Ahead: 1, Behind: 2, UpstreamGone: true, CommittedAt: at},
		{Name: "a", Scope: BranchLocal},
		{Name: "origin/a", Scope: BranchRemote},
	}, "", at)
	want := map[string]struct {
		branch, owner string
		activity      time.Time
	}{"fresh": {"a", OwnerActive, at.Add(-time.Hour)}, "detached": {"b", OwnerActive, at.Add(-time.Minute)}, "old": {"c", OwnerIdle, at.Add(-30 * time.Hour)}}
	if len(mapped.worktrees) != 3 || mapped.repository.DefaultBranch != "trunk" || mapped.repository.Host != "github.com" {
		t.Fatalf("worktrees = %d, repository %+v", len(mapped.worktrees), mapped.repository)
	}
	for _, worktree := range mapped.worktrees {
		expected := want[worktree.Task]
		if worktree.Branch != expected.branch || worktree.OwnerState != expected.owner || !worktree.LastActivityAt.Equal(expected.activity) || worktree.Lifecycle != "" || worktree.Stream != "" {
			t.Errorf("worktree %+v, want %+v", worktree, expected)
		}
	}
	if len(mapped.branches) != 2 {
		t.Fatalf("branches = %d, want the duplicate dropped", len(mapped.branches))
	}
	for _, branch := range mapped.branches {
		if branch.Scope == BranchLocal && (branch.Task != "fresh" || branch.Worktree == "" || branch.Upstream != "origin/a" || branch.Ahead != 1 || branch.Behind != 2 || !branch.UpstreamGone) {
			t.Errorf("local branch %+v", branch)
		}
		if branch.Scope == BranchRemote && (branch.Worktree != "" || branch.Task != "") {
			t.Errorf("a remote branch was tied to a worktree: %+v", branch)
		}
	}
}

// TestPullRequestRecordsAttachOnlyToAUniqueRepository covers the slug rule: a
// record is attached to its repository and worktree when exactly one local
// repository has the slug, and otherwise keeps no repository and counts as a
// diagnostic, as does a record for a repository that is not local.
func TestPullRequestRecordsAttachOnlyToAUniqueRepository(t *testing.T) {
	t.Parallel()
	sources := &fakeSources{
		repos: []discover.Repo{
			{Host: "github.com", Org: "acme", Name: "widgets", Path: t.TempDir()},
			{Host: "gitlab.com", Org: "acme", Name: "widgets", Path: t.TempDir()},
			{Host: "github.com", Org: "acme", Name: "gadgets", Path: t.TempDir()},
		},
		worktrees: map[string][]LinkedWorktree{"acme/gadgets": {{Path: "/wt/g", Branch: "g"}}},
		records:   map[string]WorktreeRecord{"/wt/g": {Task: "task-g", Branch: "g"}},
		bindings: []worktrees.RegisteredPullRequestBinding{
			{Task: "task-w", Repository: "acme/widgets", PullRequest: 1},
			{Task: "task-g", Repository: "acme/gadgets", PullRequest: 2, URL: "https://github.com/acme/gadgets/pull/2"},
			{Task: "task-n", Repository: "acme/gadgets", PullRequest: 3},
			{Task: "task-z", Repository: "elsewhere/none", PullRequest: 4},
			{Task: "task-g", Repository: "acme/gadgets", PullRequest: 2},
		},
	}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	document := snapshotter.Document()
	byNumber := map[int]PullRequest{}
	for _, pull := range document.PullRequests {
		byNumber[pull.Number] = pull
	}
	var gadgets Repository
	for _, repository := range document.Repositories {
		if repository.Name == "acme/gadgets" {
			gadgets = repository
		}
	}
	if len(document.PullRequests) != 4 || document.Diagnostics != 2 {
		t.Fatalf("%d pull requests with %d diagnostics, want 4 and 2", len(document.PullRequests), document.Diagnostics)
	}
	if byNumber[1].Repository != "" || byNumber[4].Repository != "" || byNumber[2].Repository != gadgets.ID || byNumber[2].Worktree == "" || byNumber[2].Branch != "g" || byNumber[3].Repository != gadgets.ID || byNumber[3].Worktree != "" {
		t.Errorf("pull requests = %+v", byNumber)
	}
	if byNumber[2].State != PullRequestUnknown || gadgets.OpenPullRequestCount != nil {
		t.Errorf("state %q and open pull request count %v on the repository, want unknown and no count", byNumber[2].State, gadgets.OpenPullRequestCount)
	}
}

// TestDocumentOrdersEqualNamesByIDAndHostsDoNotCollide covers one slug on two
// forges (two repositories, one entry each per collection, ordered by id) and
// two remote machines with the same name.
func TestDocumentOrdersEqualNamesByIDAndHostsDoNotCollide(t *testing.T) {
	t.Parallel()
	sources := &fakeSources{
		repos: []discover.Repo{
			{Host: "github.com", Org: "acme", Name: "widgets", Path: t.TempDir()},
			{Host: "gitlab.com", Org: "acme", Name: "widgets", Path: t.TempDir()},
		},
		worktrees: map[string][]LinkedWorktree{"acme/widgets": {{Path: "/wt/s", Branch: "same"}}},
		records:   map[string]WorktreeRecord{"/wt/s": {Task: "same", Branch: "same"}},
		branches:  map[string][]BranchRef{"acme/widgets": {{Name: "same", Scope: BranchLocal}}},
		bindings: []worktrees.RegisteredPullRequestBinding{
			{Task: "same", Repository: "acme/widgets", PullRequest: 1}, {Task: "same", Repository: "other/thing", PullRequest: 1},
		},
		remote: []remotestate.Entry{
			{Snapshot: remotestate.Snapshot{Login: "a", Machine: "dup", PublishedAt: remotePublishedAt()}},
			{Snapshot: remotestate.Snapshot{Login: "b", Machine: "dup", PublishedAt: remotePublishedAt()}},
		},
	}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	document := snapshotter.Document()
	if len(document.Repositories) != 2 || len(document.Worktrees) != 2 || len(document.Branches) != 2 || len(document.Machines) != 3 {
		t.Fatalf("document = %d repositories, %d worktrees, %d branches, %d machines",
			len(document.Repositories), len(document.Worktrees), len(document.Branches), len(document.Machines))
	}
	if len(document.PullRequests) != 2 || document.PullRequests[0].ID >= document.PullRequests[1].ID || document.Diagnostics != 2 {
		t.Errorf("pull requests = %+v, diagnostics %d", document.PullRequests, document.Diagnostics)
	}
	if document.Worktrees[0].ID >= document.Worktrees[1].ID || document.Branches[0].ID >= document.Branches[1].ID || document.Machines[0].ID >= document.Machines[1].ID {
		t.Error("entries with equal names are not ordered by id")
	}
	first, _ := snapshotter.Checkout(localRepositoryID(testMachine, sources.repos[0]))
	second, _ := snapshotter.Checkout(localRepositoryID(testMachine, sources.repos[1]))
	if first == second || first == "" {
		t.Errorf("checkouts = %q and %q, want two different clones", first, second)
	}
}

// TestSnapshotWithNoRemoteProviderHasOnlyThisMachine covers a daemon with no
// remote configured.
func TestSnapshotWithNoRemoteProviderHasOnlyThisMachine(t *testing.T) {
	t.Parallel()
	collectors := oneRepoSources(t.TempDir()).collectors()
	collectors.Remote = nil
	snapshotter, _ := newSnapshotter(collectors, nil)
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if machines := snapshotter.Document().Machines; len(machines) != 1 || machines[0].Machine != testMachine {
		t.Errorf("machines = %+v, want this machine only", machines)
	}
}

// TestCollectorPanicsMarkTheirRepositoryOrSourceAndNeverEndTheDaemon makes each
// collector panic in turn: the pass returns an error naming the failure, the
// repository carries an error code or the source keeps its last value, and the
// process is still here.
func TestCollectorPanicsMarkTheirRepositoryOrSourceAndNeverEndTheDaemon(t *testing.T) {
	t.Parallel()
	for source, want := range map[string]string{
		"repositories": "list repositories", "worktrees": ErrorReadFailed, "branches": ErrorReadFailed, "default-branch": ErrorReadFailed,
		"record": ErrorReadFailed, "sessions": "read agents", "runs": "read agents", "pull-requests": "read pull request records",
	} {
		sources := oneRepoSources(t.TempDir())
		sources.panicIn = source
		snapshotter, _ := newSnapshotter(sources.collectors(), nil)
		err := snapshotter.Refresh(t.Context())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s panic: refresh = %v, want it to name %q", source, err, want)
		}
		if source == "worktrees" || source == "branches" || source == "default-branch" || source == "record" {
			var local Repository
			for _, repository := range snapshotter.Document().Repositories {
				local = repository
			}
			if local.Error != ErrorReadFailed {
				t.Errorf("%s panic: repository = %+v, want the error code", source, local)
			}
		}
	}
	sources := oneRepoSources(t.TempDir())
	sources.panicIn = "machines"
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) { options.Logf = func(string, ...any) {} })
	refreshAndSettle(t, snapshotter)
	if machines := snapshotter.Document().Machines; len(machines) != 1 {
		t.Errorf("machines after a panicking remote read = %+v", machines)
	}
	if snapshotter.remoteBusy.Load() {
		t.Error("a panicking remote read left the remote reader marked busy")
	}
}

// TestPanicOutsideACollectorMarksTheRepositoryToo uses a panicking fingerprint,
// which runs in the pass worker itself, on a repository never scanned and on one
// already scanned.
func TestPanicOutsideACollectorMarksTheRepositoryToo(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	var explode atomic.Bool
	snapshotter, _ := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Fingerprint = func(string) (string, error) {
			if explode.Load() {
				panic("a fingerprint panicked")
			}
			return "x", nil
		}
	})
	explode.Store(true)
	if err := snapshotter.Refresh(t.Context()); !errors.Is(err, errPanicked) {
		t.Fatalf("refresh = %v, want the panic reported", err)
	}
	if got := localRepositories(snapshotter.Document()); len(got) != 1 || got[0].Error != ErrorReadFailed {
		t.Fatalf("a repository whose read panicked before it was ever scanned = %+v", got)
	}
	explode.Store(false)
	refreshAndSettle(t, snapshotter)
	explode.Store(true)
	if err := snapshotter.Refresh(t.Context()); !errors.Is(err, errPanicked) {
		t.Fatalf("refresh = %v, want the panic reported", err)
	}
	if got := localRepositories(snapshotter.Document()); len(got) != 1 || got[0].Error != ErrorReadFailed || got[0].WorktreeCount != 2 {
		t.Errorf("a scanned repository whose read panicked = %+v, want its last state with the code", got)
	}
}

func localRepositories(document Document) []Repository {
	var local []Repository
	for _, repository := range document.Repositories {
		if repository.Route == RouteLocal {
			local = append(local, repository)
		}
	}
	return local
}

// lateWorktrees returns what it read, but only after release is closed, for its
// first call.
type lateWorktrees struct {
	inner   WorktreeCollector
	first   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (l *lateWorktrees) Worktrees(ctx context.Context, repo discover.Repo) ([]LinkedWorktree, error) {
	linked, err := l.inner.Worktrees(ctx, repo)
	if l.first.CompareAndSwap(false, true) {
		close(l.entered)
		<-l.release
	}
	return linked, err
}

// TestAnOlderReadNeverOverwritesANewerOne starts a read, lets a newer read of
// the same repository finish with newer data, then lets the older read finish:
// the newer state stays.
func TestAnOlderReadNeverOverwritesANewerOne(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	late := &lateWorktrees{inner: sources, entered: make(chan struct{}), release: make(chan struct{})}
	collectors := sources.collectors()
	collectors.Worktrees = late
	snapshotter, _ := newSnapshotter(collectors, nil)
	snapshotter.track(sources.repos)
	id := localRepositoryID(testMachine, sources.repos[0])
	older := make(chan error, 1)
	go func() { older <- snapshotter.scanRepository(t.Context(), id, true) }()
	<-late.entered
	sources.change(func(f *fakeSources) {
		f.worktrees["acme/widgets"] = append(f.worktrees["acme/widgets"], LinkedWorktree{Path: "/wt/task-c", Branch: "feature/c"})
		f.records["/wt/task-c"] = WorktreeRecord{Task: "task-c", Branch: "feature/c"}
	})
	if err := snapshotter.RefreshRepository(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	close(late.release)
	if err := <-older; err != nil {
		t.Fatal(err)
	}
	snapshotter.mu.Lock()
	snapshotter.publishLocked()
	snapshotter.mu.Unlock()
	if got := len(localWorktrees(snapshotter.Document())); got != 3 {
		t.Errorf("the document lists %d worktrees after the older read finished late, want the newer read's 3", got)
	}
}

// advancingWorktrees moves the clock on every read.
type advancingWorktrees struct {
	inner WorktreeCollector
	clock *manualClock
	by    time.Duration
}

func (a advancingWorktrees) Worktrees(ctx context.Context, repo discover.Repo) ([]LinkedWorktree, error) {
	a.clock.advance(a.by)
	return a.inner.Worktrees(ctx, repo)
}

// TestPublicationsDuringAPassAreRateLimited requires at most one publication
// per publishInterval while a pass runs, plus the final one, and an
// immediate one for a request outside a pass.
func TestPublicationsDuringAPassAreRateLimited(t *testing.T) {
	t.Parallel()
	newSources := func() *fakeSources {
		sources := &fakeSources{}
		for index := range 5 {
			sources.repos = append(sources.repos, discover.Repo{Org: "acme", Name: "repo" + string(rune('a'+index)), Path: t.TempDir()})
		}
		return sources
	}
	steady := newSources()
	steadyCollectors := steady.collectors()
	steadyCollectors.Remote = nil
	snapshotter, _ := newSnapshotter(steadyCollectors, func(options *Options) { options.Workers = 1 })
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if snapshotter.publishes != 2 {
		t.Errorf("a pass over 5 repositories within one instant published %d times, want the first and the final", snapshotter.publishes)
	}
	slow := newSources()
	clock := newClock()
	collectors := slow.collectors()
	collectors.Remote = nil
	collectors.Worktrees = advancingWorktrees{inner: slow, clock: clock, by: 300 * time.Millisecond}
	paced := New(Options{Machine: testMachine, Collectors: collectors, Workers: 1, Now: clock.Now})
	if err := paced.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if paced.publishes != 6 {
		t.Errorf("a pass whose repositories each take longer than the interval published %d times, want 5 and the final", paced.publishes)
	}
	before := paced.publishes
	if err := paced.RefreshRepository(t.Context(), localRepositoryID(testMachine, slow.repos[0])); err != nil || paced.publishes != before+1 {
		t.Errorf("a request outside a pass published %d times, err %v", paced.publishes-before, err)
	}
}

// TestListingFailurePublishesAnErrorAndStillReadsAgentsAndMachines requires a
// failed repository listing to show in the document, not to leave a silent
// warm-up, to keep reading what is not per repository, and to clear when the
// next listing works.
func TestListingFailurePublishesAnErrorAndStillReadsAgentsAndMachines(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	sources.repoErr = errBoom
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	err := snapshotter.Refresh(t.Context())
	snapshotter.side.Wait()
	if !errors.Is(err, errBoom) {
		t.Fatalf("refresh = %v", err)
	}
	failed := snapshotter.Document()
	if failed.Error != ErrorRepositoriesUnreadable || !failed.WarmingUp || len(failed.Agents) != 1 || len(failed.PullRequests) != 2 || failed.SnapshotAt.IsZero() {
		t.Errorf("document after a failed listing = %+v", failed)
	}
	snapshotter.mu.Lock()
	snapshotter.publishLocked()
	snapshotter.mu.Unlock()
	if len(snapshotter.Document().Machines) != 2 {
		t.Errorf("the other machines were not read after a failed listing: %+v", snapshotter.Document().Machines)
	}
	sources.change(func(f *fakeSources) { f.repoErr = nil })
	refreshAndSettle(t, snapshotter)
	if recovered := snapshotter.Document(); recovered.Error != "" || recovered.WarmingUp {
		t.Errorf("document after the listing recovered = error %q, warming %v", recovered.Error, recovered.WarmingUp)
	}
}

// blockingRemote holds the other-machines read open until released, and counts
// its calls.
type blockingRemote struct {
	started chan struct{}
	release chan struct{}
	calls   *atomic.Int64
}

func (b blockingRemote) Machines(context.Context) ([]remotestate.Entry, error) {
	if b.calls.Add(1) == 1 {
		close(b.started)
	}
	<-b.release
	return []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "a", Machine: "desk", PublishedAt: remotePublishedAt()}}}, nil
}

// TestRemoteReadNeitherGatesTheWarmUpNorHoldsTheRefresh holds the other
// machines' read open: the pass completes, the warm-up ends, a second pass does
// not start a second read, and the machine appears when the read finishes.
func TestRemoteReadNeitherGatesTheWarmUpNorHoldsTheRefresh(t *testing.T) {
	t.Parallel()
	collectors := oneRepoSources(t.TempDir()).collectors()
	remote := blockingRemote{started: make(chan struct{}), release: make(chan struct{}), calls: &atomic.Int64{}}
	collectors.Remote = remote
	snapshotter, _ := newSnapshotter(collectors, nil)
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-remote.started
	if document := snapshotter.Document(); document.WarmingUp || len(document.Machines) != 1 {
		t.Fatalf("with the remote read held open: warming %v, %d machines", document.WarmingUp, len(document.Machines))
	}
	if err := snapshotter.Refresh(t.Context()); err != nil || remote.calls.Load() != 1 {
		t.Errorf("a second pass = %v with %d remote reads, want it not to start another", err, remote.calls.Load())
	}
	close(remote.release)
	snapshotter.side.Wait()
	if machines := snapshotter.Document().Machines; len(machines) != 2 {
		t.Errorf("machines after the remote read finished = %+v", machines)
	}
}

// TestStoppingWaitsOnlyBrieflyForARemoteReadThatIgnoresItsContext requires
// stop to return within the stop bound even when the read never finishes.
func TestStoppingWaitsOnlyBrieflyForARemoteReadThatIgnoresItsContext(t *testing.T) {
	t.Parallel()
	collectors := oneRepoSources(t.TempDir()).collectors()
	remote := blockingRemote{started: make(chan struct{}), release: make(chan struct{}), calls: &atomic.Int64{}}
	t.Cleanup(func() { close(remote.release) })
	collectors.Remote = remote
	snapshotter, _ := newSnapshotter(collectors, func(options *Options) {
		options.StopWait = 50 * time.Millisecond
		options.Tick = func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} }
	})
	stop := snapshotter.Start(t.Context())
	<-remote.started
	waitFor(t, "the first pass", func() bool { return !snapshotter.Document().WarmingUp })
	started := time.Now()
	stop()
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("stop took %v with a remote read stuck", elapsed)
	}
}

// TestAgentsAreBoundedToWhatIsLiveOrRecentAndCapped covers the filters and the
// cap: gone sessions and runs finished more than a day ago are dropped, the
// newest 200 are kept, and the document says it truncated.
func TestAgentsAreBoundedToWhatIsLiveOrRecentAndCapped(t *testing.T) {
	t.Parallel()
	at := newClock().Now()
	finished := func(ago time.Duration) *time.Time { when := at.Add(-ago); return &when }
	runs := []agents.Result{
		{AgentID: "agt-running-old", State: agents.StateRunning, StartedAt: at.Add(-72 * time.Hour)},
		{AgentID: "agt-done-recent", State: agents.StateCompleted, StartedAt: at.Add(-2 * time.Hour), FinishedAt: finished(time.Hour)},
		{AgentID: "agt-done-old", State: agents.StateCompleted, StartedAt: at.Add(-72 * time.Hour), FinishedAt: finished(48 * time.Hour)},
	}
	mapped, truncated := mapAgents(testMachine, []session.View{{Record: session.Record{WBSessionID: "wbs-gone"}, State: session.StateGone}}, runs, at)
	if len(mapped) != 2 || truncated {
		t.Fatalf("agents = %+v, truncated %v, want the running and the recently finished run", mapped, truncated)
	}
	if mapped[0].agent.RunID != "agt-done-recent" {
		t.Errorf("agents are not newest first: %+v", mapped)
	}
	var sessions []session.View
	for index := range agentCap + 50 {
		sessions = append(sessions, session.View{Record: session.Record{WBSessionID: "wbs-" + strconv.Itoa(index), StartedAt: at.Add(-time.Duration(index) * time.Minute)}, State: session.StateLive})
	}
	capped, truncated := mapAgents(testMachine, sessions, nil, at)
	if len(capped) != agentCap || !truncated || capped[0].agent.SessionID != "wbs-0" {
		t.Errorf("%d agents, truncated %v, first %q, want the newest %d", len(capped), truncated, capped[0].agent.SessionID, agentCap)
	}
	sources := oneRepoSources(t.TempDir())
	sources.sessions = sessions
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	if document := snapshotter.Document(); !document.AgentsTruncated || len(document.Agents) != agentCap {
		t.Errorf("document agents = %d, truncated %v", len(document.Agents), document.AgentsTruncated)
	}
}

// TestRemoteEntriesThatAreUnusableOrOurOwnAreSkipped covers the entries the
// remote mapping must not emit: ones with an error (including the provider's
// malformed-layout entry, whose machine is a file path), ones with no publish
// time, no machine name or a path for a name, and this machine's own
// publication, by login and name when the login is known and by name when not.
func TestRemoteEntriesThatAreUnusableOrOurOwnAreSkipped(t *testing.T) {
	t.Parallel()
	published := remotePublishedAt()
	entries := []remotestate.Entry{
		{Snapshot: remotestate.Snapshot{Login: "?", Machine: "machines/x/snapshot.yaml"}, Error: "unexpected path"},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "broken", PublishedAt: published}, Error: "bad yaml"},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "never-published"}},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "", PublishedAt: published}},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "dir/name", PublishedAt: published}},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: `dir\name`, PublishedAt: published}},
		{Snapshot: remotestate.Snapshot{Login: "me", Machine: testMachine, PublishedAt: published, ProjectsRoot: "/projects"}},
		{Snapshot: remotestate.Snapshot{Login: "someone-else", Machine: testMachine, PublishedAt: published, ProjectsRoot: "/elsewhere"}},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "desk", PublishedAt: published}},
	}
	names := func(view remoteView) string {
		var listed []string
		for _, machine := range view.machines {
			listed = append(listed, machine.Machine+"@"+machine.ObservedAt.Format(time.RFC3339))
		}
		return strings.Join(listed, ",")
	}
	byLogin := names(mapRemote(testMachine, "me", "/projects", entries))
	if byLogin != testMachine+"@"+remotePublish+",desk@"+remotePublish {
		t.Errorf("machines when the login is known = %s, want another login's machine of the same name kept", byLogin)
	}
	byRoot := names(mapRemote(testMachine, "", "/projects", entries))
	if byRoot != testMachine+"@"+remotePublish+",desk@"+remotePublish {
		t.Errorf("machines when the login is unknown = %s, want only the one with our projects root skipped", byRoot)
	}
	if byName := names(mapRemote(testMachine, "", "", entries)); !strings.Contains(byName, "desk@") || strings.Count(byName, testMachine+"@") != 2 {
		t.Errorf("machines when neither login nor projects root is known = %s, want none skipped", byName)
	}
}

// TestGitTooOldTurnsEveryGitBackedReadOff makes the version gate refuse: no
// branch or default-branch read happens, the document says why with a stable
// code, the README route says so, and the gate is asked once however many
// passes run; a panicking gate counts as refusing.
func TestGitTooOldTurnsEveryGitBackedReadOff(t *testing.T) {
	t.Parallel()
	for name, gate := range map[string]*fakeGate{"too old": {usable: false}, "panicking": {panics: true}} {
		sources := oneRepoSources(t.TempDir())
		collectors := sources.collectors()
		collectors.Git = gate
		var logged atomic.Int64
		snapshotter, _ := newSnapshotter(collectors, func(options *Options) {
			options.Logf = func(string, ...any) { logged.Add(1) }
		})
		refreshAndSettle(t, snapshotter)
		refreshAndSettle(t, snapshotter)
		document := snapshotter.Document()
		if gate.asked.Load() != 1 || logged.Load() != 1 || document.Error != ErrorGitTooOld {
			t.Errorf("%s: gate asked %d times, logged %d, document error %q", name, gate.asked.Load(), logged.Load(), document.Error)
		}
		if len(document.Branches) != 0 || len(localWorktrees(document)) != 2 {
			t.Errorf("%s: %d branches, %d worktrees; Git-backed reads must be off and file reads kept", name, len(document.Branches), len(localWorktrees(document)))
		}
		server := newCockpitServer(t, snapshotter)
		var id string
		for _, repository := range document.Repositories {
			if repository.Route == RouteLocal {
				id = repository.ID
			}
		}
		recorder := server.get(ReadmePath+"?repository="+id, server.login())
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), ErrorGitTooOld) {
			t.Errorf("%s: README = %d %s, want 503 git_too_old", name, recorder.Code, recorder.Body.String())
		}
	}
	usable := &fakeGate{usable: true}
	collectors := oneRepoSources(t.TempDir()).collectors()
	collectors.Git = usable
	snapshotter, _ := newSnapshotter(collectors, nil)
	refreshAndSettle(t, snapshotter)
	if document := snapshotter.Document(); document.Error != "" || len(document.Branches) == 0 {
		t.Errorf("with Git usable: error %q, %d branches", document.Error, len(document.Branches))
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	fresh := &fakeGate{usable: false}
	collectors.Git = fresh
	untouched, _ := newSnapshotter(collectors, nil)
	untouched.checkGit(cancelled)
	if fresh.asked.Load() != 0 || untouched.gitChecked {
		t.Error("a pass with a cancelled context asked the Git gate")
	}
}

// TestPanicsAreReportedByTypeNeverByText requires the error for a recovered
// panic to name its type and not carry the value, which could be a path.
func TestPanicsAreReportedByTypeNeverByText(t *testing.T) {
	t.Parallel()
	err := catch(func() error { panic(pathPanic{path: "/home/secret/place"}) })
	if !errors.Is(err, errPanicked) || !strings.Contains(err.Error(), "fleet.pathPanic") || strings.Contains(err.Error(), "secret") {
		t.Errorf("recovered panic = %v, want the type and no text", err)
	}
	if err := catch(func() error { return nil }); err != nil {
		t.Errorf("a clean run = %v", err)
	}
}

type pathPanic struct{ path string }

func (p pathPanic) Error() string { return "failed at " + p.path }

// TestReadmeUsesTheDocumentsDefaultBranch requires the README route to read the
// branch the document names: the listing's when it has one, else Git's.
func TestReadmeUsesTheDocumentsDefaultBranch(t *testing.T) {
	t.Parallel()
	for listed, want := range map[string]string{"": "main", "listed": "listed"} {
		sources := oneRepoSources(t.TempDir())
		sources.repos[0].DefaultBranch = listed
		snapshotter, _ := newSnapshotter(sources.collectors(), nil)
		refreshAndSettle(t, snapshotter)
		document := snapshotter.Document()
		var local Repository
		for _, repository := range document.Repositories {
			if repository.Route == RouteLocal {
				local = repository
			}
		}
		server := newCockpitServer(t, snapshotter)
		if recorder := server.get(ReadmePath+"?repository="+local.ID, server.login()); recorder.Code != http.StatusOK {
			t.Fatalf("README = %d", recorder.Code)
		}
		if sources.readmeBranch != want || local.DefaultBranch != want {
			t.Errorf("listed %q: README read %q, document says %q, want both %q", listed, sources.readmeBranch, local.DefaultBranch, want)
		}
	}
}

// TestEveryEntryCarriesItsMachinesUniqueId keeps the filter value of a machine
// unique: two logins that publish from one hostname share a name and not an id,
// and every entry names the id of the machine entry it belongs to.
func TestEveryEntryCarriesItsMachinesUniqueId(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	published := remotePublishedAt()
	worktree := func(task string) []remotestate.WorktreeState {
		return []remotestate.WorktreeState{{Task: task, Repository: "acme/far", Branch: task, PullRequest: &remotestate.PullRequestState{Number: 1, State: "OPEN"}}}
	}
	sources.remote = []remotestate.Entry{
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "dup", PublishedAt: published, Worktrees: worktree("one")}},
		{Snapshot: remotestate.Snapshot{Login: "b", Machine: "dup", PublishedAt: published, Worktrees: worktree("two")}},
	}
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	refreshAndSettle(t, snapshotter)
	document := snapshotter.Document()
	known := map[string]bool{}
	for _, machine := range document.Machines {
		if machine.MachineID != machine.ID || known[machine.ID] {
			t.Errorf("machine %+v: its machine_id must be its own, unique id", machine)
		}
		known[machine.ID] = true
	}
	if len(known) != 3 {
		t.Fatalf("machines = %+v, want this machine and two named dup", document.Machines)
	}
	ids := []string{}
	for _, entry := range document.Repositories {
		ids = append(ids, entry.MachineID)
	}
	for _, entry := range document.Worktrees {
		ids = append(ids, entry.MachineID)
	}
	for _, entry := range document.Branches {
		ids = append(ids, entry.MachineID)
	}
	for _, entry := range document.PullRequests {
		ids = append(ids, entry.MachineID)
	}
	for _, entry := range document.Agents {
		ids = append(ids, entry.MachineID)
	}
	if len(ids) < 8 {
		t.Fatalf("only %d entries to check", len(ids))
	}
	for _, id := range ids {
		if !known[id] {
			t.Errorf("machine_id %q is not a machine entry's id", id)
		}
	}
	worktreeMachines := map[string]bool{}
	for _, entry := range document.Worktrees {
		if entry.Machine == "dup" {
			worktreeMachines[entry.MachineID] = true
		}
	}
	if len(worktreeMachines) != 2 {
		t.Errorf("the two dup machines' worktrees share machine_ids %v", worktreeMachines)
	}
}

// A published document with nothing in a collection still marshals that
// collection as an empty list: the browser client rejects a null one.
func TestPublishedDocumentMarshalsEveryCollectionAsAList(t *testing.T) {
	t.Parallel()
	sources := oneRepoSources(t.TempDir())
	sources.bindings, sources.remote = nil, nil
	snapshotter, _ := newSnapshotter(sources.collectors(), nil)
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshotter.side.Wait()
	body, _ := snapshotter.Body()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"machines", "repositories", "worktrees", "branches", "pull_requests", "agents"} {
		if value := string(raw[name]); !strings.HasPrefix(value, "[") {
			t.Errorf("%s = %s, want a list", name, value)
		}
	}
}
