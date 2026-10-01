package fleet

import (
	"fmt"
	"testing"
	"time"
)

// largeDocument is a document of one machine at the caps a remote is held to:
// 2000 repositories, 2000 worktrees, 500 pull requests and 200 agents.
func largeDocument(machine string, now time.Time) Document {
	machineID := entryID(kindMachine, machine)
	entry := func(id string) Entry {
		return Entry{ID: id, Machine: machine, MachineID: machineID, Route: RouteLocal, ObservedAt: now}
	}
	document := emptyDocument(time.Minute)
	document.WarmingUp, document.SnapshotAt = false, now
	document.Machines = []Machine{{Entry: entry(machineID), WBVersion: testVersion}}
	for n := range maxLiveRepositories {
		id := entryID(kindRepository, machine, fmt.Sprint(n))
		count := 3
		document.Repositories = append(document.Repositories, Repository{Entry: entry(id), Host: "github.com", Name: fmt.Sprintf("acme/repository-%d", n), DefaultBranch: "main", LocalBranchCount: &count, RemoteURLWeb: fmt.Sprintf("https://github.com/acme/repository-%d", n)})
		ahead, upstream := 1, true
		document.Worktrees = append(document.Worktrees, Worktree{Entry: entry(entryID(kindWorktree, machine, fmt.Sprint(n))), Repository: id, Name: fmt.Sprintf("task-%d", n), Task: fmt.Sprintf("task-%d", n), Branch: fmt.Sprintf("feature/task-%d", n), OwnerState: OwnerActive, LastActivityAt: now, Ahead: &ahead, HasUpstream: &upstream})
		if n < maxLivePullRequests {
			document.PullRequests = append(document.PullRequests, PullRequest{Entry: entry(entryID(kindPR, machine, fmt.Sprint(n))), Repository: id, Number: n + 1, State: "open", URL: fmt.Sprintf("https://github.com/acme/repository-%d/pull/%d", n, n+1)})
		}
		if n < agentCap {
			document.Agents = append(document.Agents, Agent{Entry: entry(entryID(kindAgent, machine, fmt.Sprint(n))), Kind: AgentRun, RunID: fmt.Sprintf("agt-%d", n), Runtime: "claude", Model: "opus", State: "running", Repository: id})
		}
	}
	return document
}

// largeSnapshotter is a snapshotter that has published largeDocument as its own.
func largeSnapshotter() *Snapshotter {
	clock := newClock()
	snapshotter := New(Options{Machine: testMachine, Version: testVersion, Now: clock.Now})
	snapshotter.mu.Lock()
	snapshotter.store(largeDocument(testMachine, clock.Now()))
	snapshotter.publishes++
	snapshotter.mu.Unlock()
	return snapshotter
}

// BenchmarkExportRouteUncached is one request of the hub export route as it was
// before the cache: the envelope built, validated, encoded and compressed.
func BenchmarkExportRouteUncached(b *testing.B) {
	snapshotter := largeSnapshotter()
	for b.Loop() {
		snapshotter.exports = exportCache{}
		if _, failure := snapshotter.ExportPayload(false); failure != "" {
			b.Fatal(failure)
		}
	}
}

// BenchmarkExportRouteCached is one request served from the prepared payload.
func BenchmarkExportRouteCached(b *testing.B) {
	snapshotter := largeSnapshotter()
	snapshotter.ExportPayload(false)
	for b.Loop() {
		if _, failure := snapshotter.ExportPayload(false); failure != "" {
			b.Fatal(failure)
		}
	}
}

// BenchmarkLiveRemoteMergeAtTheCaps is what one accepted export of a machine at
// the caps costs outside the lock: validating it again, mapping it and
// digesting the view.
func BenchmarkLiveRemoteMergeAtTheCaps(b *testing.B) {
	now := newClock().Now()
	document := largeDocument("vm-own", now)
	envelope := Envelope{SchemaVersion: ExportSchemaVersion, Machine: "vm-own", ExportedAt: now, Fleet: &document, Metrics: exportMetrics(MetricsResponse{Route: RouteNone, Reason: ReasonNoSource})}
	if err := envelope.Validate(false, now); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		_ = envelope.Validate(false, now)
		digestOf(mapLive("vm", "mach-vm", envelope.Fleet, now, 0))
	}
}

// BenchmarkPublishWithThreeRemotesAtTheCaps is one publication of a document
// holding this machine at the caps and three live machines at the caps:
// assembled under the lock, then encoded and compressed outside it.
func BenchmarkPublishWithThreeRemotesAtTheCaps(b *testing.B) {
	snapshotter := largeSnapshotter()
	now := snapshotter.now()
	local := snapshotter.doc
	for _, key := range []string{"vm-1", "vm-2", "vm-3"} {
		document := largeDocument(key+"-own", now)
		id := entryID(kindMachine, "/"+key)
		snapshotter.live[key] = &liveMachine{target: RemoteTarget{Machine: key}, fleet: &document, receivedAt: now, observedAt: now, transport: TransportHTTP, mappedFor: id, view: mapLive(key, id, &document, now, 0)}
		snapshotter.liveKeys = append(snapshotter.liveKeys, key)
	}
	for b.Loop() {
		document := local
		hidden, failures := snapshotter.overlayLive(&document, now, true, map[string]bool{"github.com": true})
		snapshotter.appendCached(&document, hidden, failures, map[string]bool{"github.com": true})
		if payload := snapshotter.prepare(document); payload.Size() == 0 {
			b.Fatal("an empty document")
		}
	}
}
