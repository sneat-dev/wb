package fleet

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// TestAFutureDatedPublishedSnapshotIsNeverShownAsNewerThanNow holds a published
// snapshot to the time rules an export is held to: a publish time in the future
// is taken as now (it can never make the snapshot look fresh for ever), a last
// activity is never later than its snapshot and one before 2000 is dropped, and
// a snapshot published before 2000 is not used at all. A version that is not
// one and a pull request number out of range are dropped too.
func TestAFutureDatedPublishedSnapshotIsNeverShownAsNewerThanNow(t *testing.T) {
	t.Parallel()
	now := newClock().Now()
	future := now.Add(365 * 24 * time.Hour)
	ancient := time.Date(1999, 12, 31, 0, 0, 0, 0, time.UTC)
	pull := func(number int) *remotestate.PullRequestState {
		return &remotestate.PullRequestState{Number: number, State: "open", URL: "https://github.com/o/r/pull/1"}
	}
	view := mapRemoteForTest("", "", []remotestate.Entry{
		{Snapshot: remotestate.Snapshot{
			Login: "a", Machine: "ahead", PublishedAt: future, WBVersion: "v1.2.3 && rm -rf /",
			Worktrees: []remotestate.WorktreeState{
				{Task: "later", Repository: "o/r", Branch: "later", LastActivityAt: future.Add(time.Hour), PullRequest: pull(0)},
				{Task: "ancient", Repository: "o/r", Branch: "ancient", LastActivityAt: ancient, PullRequest: pull(-4)},
				{Task: "earlier", Repository: "o/r", Branch: "earlier", LastActivityAt: now.Add(-time.Hour), PullRequest: pull(maxCount + 1)},
				{Task: "counted", Repository: "o/r", Branch: "counted", PullRequest: pull(maxCount)},
			},
		}},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "before-2000", PublishedAt: ancient}},
		{Snapshot: remotestate.Snapshot{Login: "a", Machine: "proper", PublishedAt: now.Add(-time.Hour), WBVersion: "v0.174.0"}},
	})
	if len(view.machines) != 2 {
		t.Fatalf("machines = %+v, want the one published before 2000 left out", view.machines)
	}
	versions := map[string]string{}
	for _, machine := range view.machines {
		versions[machine.Machine] = machine.WBVersion
		if machine.ObservedAt.After(now) {
			t.Errorf("%s is observed at %s, after now", machine.Machine, machine.ObservedAt)
		}
	}
	if versions["ahead"] != "" || versions["proper"] != "v0.174.0" {
		t.Errorf("versions = %v, want the one that is not a version dropped", versions)
	}
	activity := map[string]time.Time{}
	for _, worktree := range view.worktrees {
		activity[worktree.Task] = worktree.LastActivityAt
		if !worktree.ObservedAt.Equal(now) {
			t.Errorf("worktree %s is observed at %s, want now for a snapshot published in the future", worktree.Task, worktree.ObservedAt)
		}
	}
	if !activity["later"].Equal(now) || !activity["ancient"].IsZero() || !activity["earlier"].Equal(now.Add(-time.Hour)) {
		t.Errorf("last activity = %v, want a future one taken as the snapshot's time, one before 2000 dropped, a plausible one kept", activity)
	}
	if len(view.pullRequests) != 1 || view.pullRequests[0].Number != maxCount {
		t.Errorf("pull requests = %+v, want only the one whose number is within range", view.pullRequests)
	}
}

// TestAnOversizedPublishedSnapshotIsCutAtTheLiveCaps holds a published snapshot
// to the caps of a live export: at most 2000 repositories, 2000 worktrees and
// 500 pull requests of one machine, the same ones on every read, the rest
// counted in the machine's export_dropped; and at most maxCachedMachines
// machines, the newest publications, with one diagnostic in the document.
func TestAnOversizedPublishedSnapshotIsCutAtTheLiveCaps(t *testing.T) {
	t.Parallel()
	now := newClock().Now()
	big := remotestate.Snapshot{Login: "a", Machine: "big", PublishedAt: now.Add(-time.Hour)}
	for index := range maxLiveRepositories + 50 {
		big.KnownRepositories = append(big.KnownRepositories, fmt.Sprintf("o/known-%04d", index))
	}
	// 2100 worktrees of one repository that sorts first, each with an open pull request.
	for index := range maxLiveWorktrees + 100 {
		big.Worktrees = append(big.Worktrees, remotestate.WorktreeState{
			Task: fmt.Sprintf("t%04d", index), Repository: "a/first", Branch: fmt.Sprintf("b%04d", index),
			PullRequest: &remotestate.PullRequestState{Number: index + 1, State: "open"},
		})
	}
	// One worktree of a repository that the cap cuts.
	big.Worktrees = append(big.Worktrees, remotestate.WorktreeState{Task: "cut", Repository: "z/last", Branch: "cut"})
	entries := []remotestate.Entry{{Snapshot: big}}
	view := mapRemoteForTest("", "", entries)
	// 52 repositories over the cap (2050 known and the two the worktrees name), 100
	// worktrees over theirs and the one of a repository that was cut, and the
	// pull requests of the kept worktrees over theirs.
	wantCut := 52 + 100 + 1 + (maxLiveWorktrees - maxLivePullRequests)
	if len(view.repositories) != maxLiveRepositories || len(view.worktrees) != maxLiveWorktrees || len(view.pullRequests) != maxLivePullRequests {
		t.Fatalf("kept %d repositories, %d worktrees, %d pull requests", len(view.repositories), len(view.worktrees), len(view.pullRequests))
	}
	if machine := view.machines[0]; machine.ExportDropped != wantCut || machine.RepositoryCount != maxLiveRepositories || machine.WorktreeCount != maxLiveWorktrees {
		t.Fatalf("the machine = %+v, want export_dropped %d", machine, wantCut)
	}
	again := mapRemoteForTest("", "", entries)
	for index := range view.repositories {
		if view.repositories[index].ID != again.repositories[index].ID {
			t.Fatal("the cap keeps different repositories on a second read")
		}
	}
	for _, worktree := range view.worktrees {
		if worktree.Repository == "" || worktree.Task == "cut" {
			t.Fatalf("a worktree of a repository that was cut is kept: %+v", worktree)
		}
	}

	var many []remotestate.Entry
	for index := range maxCachedMachines + 3 {
		many = append(many, remotestate.Entry{Snapshot: remotestate.Snapshot{
			Login: "a", Machine: fmt.Sprintf("m%03d", index), PublishedAt: now.Add(-time.Duration(index+1) * time.Minute),
		}})
	}
	crowd := mapRemoteForTest("", "", many)
	if len(crowd.machines) != maxCachedMachines || crowd.machinesCut != 3 {
		t.Fatalf("%d machines kept, %d cut", len(crowd.machines), crowd.machinesCut)
	}
	for _, machine := range crowd.machines {
		if machine.Machine >= fmt.Sprintf("m%03d", maxCachedMachines) {
			t.Fatalf("the oldest publication %s was kept over a newer one", machine.Machine)
		}
	}
	snapshotter, _ := newSnapshotter((&fakeSources{remote: many}).collectors(), nil)
	refreshAndSettle(t, snapshotter)
	if document := snapshotter.Document(); document.Diagnostics != 1 || len(document.Machines) != maxCachedMachines+1 {
		t.Fatalf("the document has %d machines and %d diagnostics, want the cut counted once", len(document.Machines), document.Diagnostics)
	}
}

// TestTheSizeBoundCoversThePublishedEntriesToo proves that the document's size
// guard counts the published store's entries, not only the live ones: a
// document that would be over the bound with them is published with each
// published machine's own entry alone, carrying export_too_large, one more
// diagnostic and one log line, and the entries return when they fit again.
func TestTheSizeBoundCoversThePublishedEntriesToo(t *testing.T) {
	t.Parallel()
	now := newClock().Now()
	heavy := remotestate.Snapshot{Login: "a", Machine: "heavy", PublishedAt: now.Add(-time.Hour)}
	for index := range 400 {
		heavy.Worktrees = append(heavy.Worktrees, remotestate.WorktreeState{Task: fmt.Sprintf("task-%04d", index), Repository: "o/r", Branch: strings.Repeat("b", 100) + fmt.Sprint(index)})
	}
	logs := &logRecorder{}
	sources := oneRepoSources("/repos/widgets")
	snapshotter, clock := newSnapshotter(sources.collectors(), func(options *Options) { options.Logf = logs.logf })
	refreshAndSettle(t, snapshotter)
	alone := snapshotter.Payload().Size()
	diagnostics := snapshotter.Document().Diagnostics
	snapshotter.mu.Lock()
	snapshotter.maxDocument = alone + 2000
	snapshotter.mu.Unlock()
	sources.change(func(sources *fakeSources) { sources.remote = []remotestate.Entry{{Snapshot: heavy}} })
	clock.advance(time.Second)
	refreshAndSettle(t, snapshotter)
	refreshAndSettle(t, snapshotter)
	document := snapshotter.Document()
	machine, found := machineNamed(document, "heavy")
	if !found || machine.Route != RouteCached || machine.RemoteError != RemoteErrorExportTooLarge || machine.WorktreeCount != 400 {
		t.Fatalf("the published machine = %+v (found %v), want its entry alone with export_too_large", machine, found)
	}
	if entries := entriesOf(document, machine.ID); len(entries) != 0 || document.Diagnostics != diagnostics+1 || snapshotter.Payload().Size() > alone+2000 {
		t.Fatalf("entries %v, diagnostics %d, %d bytes, want none of its entries within the bound", entries, document.Diagnostics, snapshotter.Payload().Size())
	}
	if logs.count("the other machines' published entries") != 1 {
		t.Errorf("log = %q, want the guard logged once", logs.all())
	}
	snapshotter.mu.Lock()
	snapshotter.maxDocument = defaultMaxDocumentBytes
	snapshotter.mu.Unlock()
	clock.advance(time.Second)
	refreshAndSettle(t, snapshotter)
	document = snapshotter.Document()
	if machine, _ := machineNamed(document, "heavy"); machine.RemoteError != "" || len(entriesOf(document, machine.ID)["worktrees"]) != 400 || document.Diagnostics != diagnostics {
		t.Fatalf("when it fits again: %+v with %d worktrees and %d diagnostics", machine, len(entriesOf(document, machine.ID)["worktrees"]), document.Diagnostics)
	}
}
