package remotepublish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/periodic"
)

// capturingProvider is the fake remote store of the periodic publisher: it
// counts publishes and keeps what it was given, and touches no network or Git.
type capturingProvider struct {
	remotestate.Provider
	mu   sync.Mutex
	seen []remotestate.Snapshot
	errs []error
}

func (p *capturingProvider) Publish(_ context.Context, snapshot remotestate.Snapshot) (remotestate.PublishResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, snapshot)
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return remotestate.PublishResult{}, err
	}
	return remotestate.PublishResult{Location: "sha"}, nil
}

// fixedSource is a fake publish source.
type fixedSource struct {
	extras remotestate.Extras
	token  string
}

func (f fixedSource) PublishExtras() remotestate.Extras { return f.extras }
func (f fixedSource) ChangeToken() string               { return f.token }

func publishDeps(provider remotestate.Provider, login func() (string, error), opened *int) Dependencies {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	deps := DefaultDependencies("", testExitError)
	supplied := Dependencies{
		Login: login, Now: func() time.Time { return now },
		Open: func(remotestate.Config, string) (remotestate.Provider, error) {
			*opened++
			return provider, nil
		},
	}
	deps.Login, deps.Now, deps.Open = supplied.Login, supplied.Now, supplied.Open
	return deps
}

func publishConfig(publish remotestate.PublishConfig) remotestate.Config {
	return remotestate.Config{Provider: "git", Repo: "acme/wb-state", Machine: "mac", Publish: publish}
}

// TestCockpitFleetOptionsPublishOnlyWhenTheOwnerOptedIn proves the opt-in
// defaults of cockpit-views#req:periodic-remote-publish: a machine that only
// ever published by hand, or has no store, or sets a bad interval, has no
// publisher, and one that sets an interval has one.

func TestPeriodicPublisherUsesTheCLIsIdentityScanAndProvider(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	opened, logins := 0, 0
	deps := publishDeps(provider, func() (string, error) { logins++; return "alice", nil }, &opened)
	cfg := publishConfig(remotestate.PublishConfig{Interval: time.Minute, Agents: true, Metrics: true, Unpushed: remotestate.RedactUnpushed})
	var logs bytes.Buffer
	learned := &testLearnedLogin{}
	if learned.known() != "" {
		t.Fatal("a login is known before anything resolved it")
	}
	publisher := New(deps).Periodic(cfg, t.TempDir(), func(format string, args ...any) { logs.WriteString(format) }, learned.learn)
	extras := fixedSource{extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", RunID: "agt-1", State: "running"}}, Metrics: &remotestate.MetricsSample{SampledAt: deps.Now()}}}
	publisher.Publish(t.Context(), extras)
	if len(provider.seen) != 1 || opened != 1 || logins != 1 {
		t.Fatalf("published %d, opened %d, logins %d (logs %q)", len(provider.seen), opened, logins, logs.String())
	}
	// The login the publisher resolved is what the fleet snapshotter tells this
	// machine's own publications by.
	if learned.known() != "alice" {
		t.Fatalf("the daemon learned the login %q, want alice", learned.known())
	}
	got := provider.seen[0]
	if got.Login != "alice" || got.Machine != "mac" || got.RemoteStore != "git:acme/wb-state" || got.WBVersion == "" || !got.PublishedAt.Equal(deps.Now()) {
		t.Fatalf("identity = %+v", got)
	}
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH || got.CPUCount != runtime.NumCPU() {
		t.Errorf("hardware = %q %q %d", got.OS, got.Arch, got.CPUCount)
	}
	if len(got.Agents) != 1 || got.Metrics == nil || got.SchemaVersion != remotestate.SchemaVersion {
		t.Errorf("agents %d, metrics %v, schema %d", len(got.Agents), got.Metrics, got.SchemaVersion)
	}
	// The login is looked up once, not on every attempt.
	if status := publisher.Status(); status.Published != 1 || status.Diagnostic != periodic.DiagnosticNone {
		t.Fatalf("status = %+v", status)
	}
}

func TestPeriodicPublisherWithoutTheFlagsPublishesNeitherAgentsNorMetrics(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	opened := 0
	cfg := publishConfig(remotestate.PublishConfig{Interval: 10 * time.Minute})
	publisher := New(publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)).Periodic(cfg, t.TempDir(), nil, nil)
	publisher.Publish(t.Context(), fixedSource{extras: remotestate.Extras{Agents: []remotestate.AgentState{{Kind: "run", State: "running"}}, Metrics: &remotestate.MetricsSample{}}})
	if len(provider.seen) != 1 || provider.seen[0].Agents != nil || provider.seen[0].Metrics != nil {
		t.Fatalf("seen = %+v", provider.seen)
	}
	// Hardware is part of the machine entry and is published regardless.
	if provider.seen[0].OS == "" {
		t.Error("the machine entry lacks its hardware")
	}
}

func TestPeriodicPublisherFailsTypedWhenTheLoginOrTheStoreIsUnavailable(t *testing.T) {
	t.Parallel()
	opened := 0
	provider := &capturingProvider{}
	noLogin := New(publishDeps(provider, func() (string, error) { return "", errors.New("gh: not logged in") }, &opened)).Periodic(publishConfig(remotestate.PublishConfig{Interval: time.Hour}), t.TempDir(), nil, nil)
	noLogin.Publish(t.Context(), nil)
	if status := noLogin.Status(); status.Diagnostic != periodic.DiagnosticCollectFailed || opened != 0 || len(provider.seen) != 0 {
		t.Fatalf("status = %+v, opened %d", status, opened)
	}
	blank := New(publishDeps(provider, func() (string, error) { return "", nil }, &opened)).Periodic(publishConfig(remotestate.PublishConfig{Interval: time.Hour}), t.TempDir(), nil, nil)
	blank.Publish(t.Context(), nil)
	if status := blank.Status(); status.Diagnostic != periodic.DiagnosticCollectFailed {
		t.Fatalf("a blank login = %+v", status)
	}
	closed, cancel := context.WithCancel(t.Context())
	cancel()
	cancelled := New(publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)).Periodic(publishConfig(remotestate.PublishConfig{Interval: time.Hour}), t.TempDir(), nil, nil)
	cancelled.Publish(closed, nil)
	if status := cancelled.Status(); status.Diagnostic != periodic.DiagnosticCollectFailed || len(provider.seen) != 0 {
		t.Fatalf("a cancelled scan = %+v", status)
	}
}

func TestAPeriodicScanReadsWithGitOnlyTheRepositoriesThatChanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	widgets, gadgets, gizmos := fakeClone(t, root, "widgets"), fakeClone(t, root, "gadgets"), fakeClone(t, root, "gizmos")
	provider := &capturingProvider{}
	opened := 0
	deps := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	clock := deps.Now()
	deps.Now = func() time.Time { return clock }
	var mu sync.Mutex
	reads := map[string]int{}
	var failing string
	deps.ReadRepository = func(path string) (gitops.RepoStatus, gitops.TrackingState, error) {
		mu.Lock()
		defer mu.Unlock()
		reads[path]++
		if path == failing {
			return gitops.RepoStatus{}, gitops.TrackingState{}, errors.New("git failed")
		}
		// Each read finds one more untracked file: what is published says which
		// read it came from.
		return gitops.RepoStatus{Untracked: make([]string, reads[path])}, gitops.TrackingState{Branch: "main"}, nil
	}
	publisher := New(deps).Periodic(publishConfig(remotestate.PublishConfig{Interval: 5 * time.Minute, Unpushed: remotestate.RedactNone}), root, nil, nil)
	attempt := func(advance time.Duration) map[string]int {
		t.Helper()
		clock = clock.Add(advance)
		before := len(provider.seen)
		publisher.Publish(t.Context(), nil)
		if len(provider.seen) != before+1 {
			t.Fatalf("nothing was published: %+v", publisher.Status())
		}
		untracked := map[string]int{}
		for _, repository := range provider.seen[before].Repositories {
			untracked[filepath.Base(repository.Path)] = len(repository.Untracked)
			if repository.Error != "" {
				untracked[filepath.Base(repository.Path)] = -1
			}
		}
		return untracked
	}
	total := func() int {
		mu.Lock()
		defer mu.Unlock()
		return reads[widgets] + reads[gadgets] + reads[gizmos]
	}
	if got := attempt(0); total() != 3 || got["widgets"] != 1 || got["gadgets"] != 1 || got["gizmos"] != 1 {
		t.Fatalf("the first scan read %d repositories and published %v", total(), got)
	}
	// One repository changed: it alone is read, and the others are published as
	// they were read.
	moveRef(t, gadgets, "1")
	if got := attempt(6 * time.Minute); total() != 4 || reads[gadgets] != 2 || got["gadgets"] != 2 || got["widgets"] != 1 || got["gizmos"] != 1 {
		t.Fatalf("after one repository changed: %d reads (%v), published %v", total(), reads, got)
	}
	// A read that fails is published as the error it is and is not kept: the
	// repository is read again at the next scan, though its fingerprint is the same.
	moveRef(t, gizmos, "1")
	failing = gizmos
	if got := attempt(6 * time.Minute); total() != 5 || got["gizmos"] != -1 {
		t.Fatalf("a failed read: %d reads, published %v", total(), got)
	}
	failing = ""
	moveRef(t, widgets, "1")
	if got := attempt(6 * time.Minute); total() != 7 || reads[gizmos] != 3 || got["gizmos"] != 3 || got["widgets"] != 2 || got["gadgets"] != 2 {
		t.Fatalf("after the failed read: %d reads (%v), published %v", total(), reads, got)
	}
	// What is kept is never as old as the keepalive: the keepalive's scan reads
	// every repository, which is how a change no fingerprint sees is published.
	if got := attempt(periodic.DefaultKeepalive); total() != 10 || got["widgets"] != 3 || got["gadgets"] != 3 || got["gizmos"] != 4 {
		t.Fatalf("the keepalive's scan: %d reads (%v), published %v", total(), reads, got)
	}
}

func TestRepositoryScansKeepNothingTheyCannotVouchFor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	reads := 0
	fingerprint, fingerprintErr := "a", error(nil)
	scans := &repositoryScans{
		read: func(string) (gitops.RepoStatus, gitops.TrackingState, error) {
			reads++
			return gitops.RepoStatus{}, gitops.TrackingState{Branch: "main"}, nil
		},
		fingerprint: func(string) (string, error) { return fingerprint, fingerprintErr },
		maxAge:      time.Hour,
	}
	for range 2 {
		if _, tracking, err := scans.of("/repos/widgets", now); err != nil || tracking.Branch != "main" {
			t.Fatalf("a read: %+v %v", tracking, err)
		}
	}
	if reads != 1 {
		t.Fatalf("an unchanged clone was read %d times", reads)
	}
	fingerprintErr = errors.New("no .git")
	for range 2 {
		if _, _, err := scans.of("/repos/widgets", now); err != nil {
			t.Fatal(err)
		}
	}
	fingerprintErr = nil
	if _, _, _ = scans.of("/repos/widgets", now); reads != 4 {
		t.Fatalf("a clone with no fingerprint: %d reads, want one for each scan and one after, since nothing of it was kept", reads)
	}
	scans.keepOnly(map[string]bool{"/repos/gadgets": true})
	if _, _, _ = scans.of("/repos/widgets", now); reads != 5 {
		t.Fatalf("a repository that left the fleet was kept: %d reads", reads)
	}
	// No keeper: Git reads, here a directory that is no repository.
	var none *repositoryScans
	none.keepOnly(nil)
	if _, _, err := none.of(t.TempDir(), now); err == nil {
		t.Fatal("a directory that is no repository was read without an error")
	}
}

func TestRepositoryScansNameTheOldestScanTheyKeep(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	scans := &repositoryScans{
		read: func(string) (gitops.RepoStatus, gitops.TrackingState, error) {
			return gitops.RepoStatus{}, gitops.TrackingState{}, nil
		},
		fingerprint: func(string) (string, error) { return "a", nil },
		maxAge:      time.Hour,
	}
	var none *repositoryScans
	if !none.oldest().IsZero() || !scans.oldest().IsZero() {
		t.Fatal("a keeper with nothing kept named a time")
	}
	for i, path := range []string{"/repos/a", "/repos/b", "/repos/c"} {
		if _, _, err := scans.of(path, start.Add([]time.Duration{10 * time.Minute, 0, 20 * time.Minute}[i])); err != nil {
			t.Fatal(err)
		}
	}
	if got := scans.oldest(); !got.Equal(start) {
		t.Fatalf("oldest = %v, want %v", got, start)
	}
	scans.keepOnly(map[string]bool{"/repos/a": true, "/repos/c": true})
	if got := scans.oldest(); !got.Equal(start.Add(10 * time.Minute)) {
		t.Fatalf("after the oldest left the fleet, oldest = %v", got)
	}
}

func TestThePeriodicPublishSaysOnceInTheLogThatHardwareIsIncluded(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{errs: []error{errors.New("down")}}
	opened := 0
	deps := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	deps.ConfigPath = cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n  machine: mac\n")()
	clock := deps.Now()
	deps.Now = func() time.Time { return clock }
	var logs []string
	publisher := New(deps).Periodic(publishConfig(remotestate.PublishConfig{Interval: 5 * time.Minute}), t.TempDir(), func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}, nil)
	noted := func() int {
		count := 0
		for _, line := range logs {
			if line == periodicHardwareNote {
				count++
			}
		}
		return count
	}
	publisher.Publish(t.Context(), nil) // the store is down: nothing was sent, nothing is said
	if noted() != 0 {
		t.Fatalf("a publish that failed used the note up: %q", logs)
	}
	for range 2 {
		clock = clock.Add(7 * time.Hour)
		publisher.Publish(t.Context(), nil)
	}
	if publisher.Status().Published != 2 || noted() != 1 {
		t.Fatalf("after two publishes the note was logged %d times: %q (%+v)", noted(), logs, publisher.Status())
	}
	var progress bytes.Buffer
	if _, err := New(deps).Publish(Request{ProjectsRoot: t.TempDir(), Parallel: 1}, Progress{}, &progress); err != nil || strings.Contains(progress.String(), "boot_time") {
		t.Fatalf("a publish by hand after the daemon's said the note again: %v %q", err, progress.String())
	}
	// With no log and no configuration nothing breaks, and nothing is recorded.
	notePeriodicHardware("", nil)
	silent := cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n")()
	notePeriodicHardware(silent, nil)
	if hardwareNoteMarker(silent) != "" {
		t.Fatal("the note was not recorded as said")
	}
}

func TestCollectSnapshotStopsWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	closed, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := New(DefaultDependencies("", testExitError)).collectSnapshot(closed, t.TempDir(), "", 1, remotestate.Snapshot{}, remotestate.RedactNone, Progress{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// With a repository to read, a scan that was cancelled reads none of them
	// (no Git runs: the check comes first) and still reports the cancellation.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "acme", "widgets", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(DefaultDependencies("", testExitError)).collectSnapshot(closed, root, "", 1, remotestate.Snapshot{}, remotestate.RedactNone, Progress{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("a scan with a repository: err = %v, want context.Canceled", err)
	}
}

func TestPublishIdentityCarriesTheHardwareFacts(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	identity := New(DefaultDependencies("", testExitError)).publishIdentity(publishConfig(remotestate.PublishConfig{}), "alice", at)
	if identity.Login != "alice" || identity.Machine != "mac" || !identity.PublishedAt.Equal(at) || identity.OS == "" || identity.CPUCount < 1 || identity.Agents != nil || identity.Metrics != nil {
		t.Fatalf("identity = %+v", identity)
	}
}
