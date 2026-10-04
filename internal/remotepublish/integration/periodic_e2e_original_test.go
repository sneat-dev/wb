//go:build e2e

package integration

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/cockpitoptions"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/periodic"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestE2EPeriodicPublishToAGitStoreCommitsOnlyWhatChanged runs the periodic
// publisher against the git provider and a temporary bare repository (no
// network, no real store): an idle machine adds no commit at each interval,
// a changed one adds exactly one, and the keepalive adds one after six hours.
func TestE2EPeriodicPublishToAGitStoreCommitsOnlyWhatChanged(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	deps := f.deps("alice", clock)
	deps.Now = func() time.Time { return clock }
	cfg := remotestate.Config{Provider: "git", Repo: "team/wb-state", Machine: "laptop", Publish: remotestate.PublishConfig{Interval: 5 * time.Minute, Unpushed: remotestate.RedactNone}}
	publisher := remotepublish.New(deps.Dependencies).Periodic(cfg, f.projectsRoot, nil, nil)
	// The real gate source: the fleet snapshotter of the daemon over the same
	// projects root, refreshed before each attempt as its own ticker would.
	options := cockpitoptions.Options(cockpitoptions.Request{ProjectsRoot: f.projectsRoot, Home: t.TempDir(), ConfigPath: filepath.Join(t.TempDir(), "absent.yaml"), Config: wbconfig.DefaultCockpitConfig(), Logs: io.Discard}, testCockpitDependencies("laptop"))
	options.PullRequests = nil
	snapshotter := cockpitfleet.New(options)
	commits := func() int {
		count, err := strconv.Atoi(strings.TrimSpace(remoteGit(t, f.origin, "rev-list", "--count", "main")))
		if err != nil {
			t.Fatal(err)
		}
		return count
	}
	attempt := func(advance time.Duration) {
		clock = clock.Add(advance)
		if err := snapshotter.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		publisher.Publish(t.Context(), snapshotter)
	}
	base := commits() // the seed commit
	attempt(0)
	if got := commits(); got != base+1 {
		t.Fatalf("first publish: %d commits, want %d (status %+v)", got, base+1, publisher.Status())
	}
	stored := remoteGit(t, f.origin, "show", "main:machines/alice/laptop/snapshot.yaml")
	if !strings.Contains(stored, "os: ") || !strings.Contains(stored, "cpu_count:") || strings.Contains(stored, "agents:") || strings.Contains(stored, "metrics:") {
		t.Fatalf("stored snapshot should carry the hardware and neither agents nor metrics:\n%s", stored)
	}
	// The first publish created the clone of the store under the projects root,
	// which the next scan lists as a repository: one more commit, once.
	attempt(6 * time.Minute)
	attempt(6 * time.Minute)
	settled := commits()
	if settled < base+2 {
		t.Fatalf("the store's own clone appearing in the scan: %d commits, want at least %d", settled, base+2)
	}
	// An idle machine: five more intervals, no commit, and most do not even scan.
	status := publisher.Status()
	for range 5 {
		attempt(6 * time.Minute)
	}
	after := publisher.Status()
	if got := commits(); got != settled || after.Published != status.Published || after.Gated+after.Skipped != status.Gated+status.Skipped+5 {
		t.Fatalf("idle machine: %d commits (was %d), status %+v then %+v", got, settled, status, after)
	}
	// A change that moves a fingerprint (a commit in the repository) is one commit,
	// found through the real gate.
	remoteGit(t, filepath.Join(f.projectsRoot, "acme", "widgets"), "commit", "-q", "--allow-empty", "-m", "more")
	attempt(6 * time.Minute)
	if got := commits(); got != settled+1 {
		t.Fatalf("changed machine: %d commits, want %d (status %+v)", got, settled+1, publisher.Status())
	}
	// Idle for seven hours: nothing at five hours, the keepalive at seven.
	attempt(5 * time.Hour)
	if got := commits(); got != settled+1 {
		t.Fatalf("idle for five hours: %d commits, want %d", got, settled+1)
	}
	attempt(2 * time.Hour)
	if got := commits(); got != settled+2 || publisher.Status().Diagnostic != periodic.DiagnosticNone {
		t.Fatalf("keepalive: %d commits, status %+v", got, publisher.Status())
	}
}

// TestE2EPeriodicPublishPreGateOnA400RepositoryFleet measures one periodic
// attempt on a fixture of 400 real repositories, with and without the
// snapshotter's change-token gate, and requires the gated attempt to scan
// nothing. The numbers are logged (go test -v) for the report.
func TestE2EPeriodicPublishPreGateOnA400RepositoryFleet(t *testing.T) {
	setGitIdentity(t)
	root := t.TempDir()
	const repositories = 400
	for index := range repositories {
		repo := filepath.Join(root, "acme", "repo"+strconv.Itoa(index))
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		remoteGit(t, repo, "init", "-q", "-b", "main")
		remoteGit(t, repo, "commit", "-q", "--allow-empty", "-m", "seed")
	}
	options := cockpitoptions.Options(cockpitoptions.Request{ProjectsRoot: root, Home: t.TempDir(), ConfigPath: filepath.Join(t.TempDir(), "absent.yaml"), Config: wbconfig.DefaultCockpitConfig(), Logs: io.Discard}, testCockpitDependencies("host"))
	options.PullRequests = nil
	snapshotter := cockpitfleet.New(options)
	began := time.Now()
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Logf("fleet snapshotter: one full pass over %d repositories took %s", repositories, time.Since(began).Round(time.Millisecond))
	if snapshotter.ChangeToken() == "" {
		t.Fatal("no change token after a full pass")
	}

	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	build := func() (*periodic.Publisher, *capturingProvider) {
		provider := &capturingProvider{}
		opened := 0
		deps := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
		deps.Now = func() time.Time { return clock }
		cfg := publishConfig(remotestate.PublishConfig{Interval: 5 * time.Minute, Unpushed: remotestate.RedactNone})
		return remotepublish.New(deps.Dependencies).Periodic(cfg, root, nil, nil), provider
	}
	timed := func(publisher *periodic.Publisher, source remotestate.PublishSource) time.Duration {
		clock = clock.Add(6 * time.Minute)
		started := time.Now()
		publisher.Publish(t.Context(), source)
		return time.Since(started)
	}

	ungated, ungatedProvider := build()
	first := timed(ungated, nil)
	idleWithoutGate := timed(ungated, nil)
	if len(ungatedProvider.seen) != 1 || ungated.Status().Skipped != 1 {
		t.Fatalf("without the gate: published %d, %+v", len(ungatedProvider.seen), ungated.Status())
	}
	gated, gatedProvider := build()
	timed(gated, snapshotter)
	idleWithGate := timed(gated, snapshotter)
	if len(gatedProvider.seen) != 1 || gated.Status().Gated != 1 || gated.Status().Attempts != 1 {
		t.Fatalf("with the gate: published %d, %+v", len(gatedProvider.seen), gated.Status())
	}
	t.Logf("first publish (scan + publish): %s", first.Round(time.Millisecond))
	t.Logf("idle attempt without the gate (scan, digest skip): %s", idleWithoutGate.Round(time.Millisecond))
	t.Logf("idle attempt with the gate (no scan): %s", idleWithGate.Round(time.Microsecond))
	if idleWithGate*20 > idleWithoutGate {
		t.Fatalf("the gate saved too little: %s against %s", idleWithGate, idleWithoutGate)
	}
	// A change that moves a fingerprint opens the gate again.
	remoteGit(t, filepath.Join(root, "acme", "repo0"), "commit", "-q", "--allow-empty", "-m", "more")
	if err := snapshotter.RefreshRepository(t.Context(), localRepoID(t, snapshotter, "repo0")); err != nil {
		t.Fatal(err)
	}
	changed := timed(gated, snapshotter)
	if status := gated.Status(); status.Attempts != 2 || status.Published != 2 || status.Scans != 2 {
		t.Fatalf("a changed repository did not open the gate: %+v", status)
	}
	// That scan read with Git the one repository that changed, not the fleet.
	t.Logf("attempt after one repository of %d changed (scan + publish): %s", repositories, changed.Round(time.Millisecond))
	if changed*2 > first {
		t.Fatalf("a change of one repository cost %s, against %s for the scan of them all", changed, first)
	}
	// What is left of such an attempt is not the repositories' Git status: it is
	// the listing of the repositories and the worktree inventory, which reads
	// state no fingerprint covers (heartbeats, manifests, owners).
	began = time.Now()
	if _, err := reposelection.Select(reposelection.Request{ProjectsRoot: root, Fleet: true, Parallel: 2, AllowEmpty: true}); err != nil {
		t.Fatal(err)
	}
	t.Logf("of which listing the repositories: %s", time.Since(began).Round(time.Millisecond))
	began = time.Now()
	if _, err := worktrees.List(t.Context(), worktrees.ListOptions{ProjectsRoot: root}); err != nil {
		t.Fatal(err)
	}
	t.Logf("of which the worktree inventory: %s", time.Since(began).Round(time.Millisecond))
}

// localRepoID is the id of the local repository named name in the document.
func localRepoID(t *testing.T, snapshotter *cockpitfleet.Snapshotter, name string) string {
	t.Helper()
	var document cockpitfleet.Document
	if err := json.Unmarshal(fleetBody(snapshotter), &document); err != nil {
		t.Fatal(err)
	}
	for _, repository := range document.Repositories {
		if strings.HasSuffix(repository.Name, "/"+name) {
			return repository.ID
		}
	}
	t.Fatalf("no repository %s", name)
	return ""
}
