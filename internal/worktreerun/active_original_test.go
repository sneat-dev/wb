package worktreerun

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type remoteDeps struct {
	configPath string
	login      func() (string, error)
	open       func(remotestate.Config, string) (remotestate.Provider, error)
	now        func() time.Time
}

func (remote remoteDeps) domain() ActiveRemoteDependencies {
	return ActiveRemoteDependencies{Load: func(root string) (remotestate.Config, remotestate.Provider, error) {
		return remotepublish.Load(remote.configPath, root, remote.open, func(code int, message string) error { return fmt.Errorf("exit %d: %s", code, message) })
	}, Login: remote.login, Now: remote.now}
}

type activeWorktreeDeps struct {
	claims   func(string, string) ([]worktrees.ActiveClaimSummary, error)
	sessions func(string) ([]session.View, error)
	remote   remoteDeps
}

func runWorktreeActive(ctx context.Context, deps activeWorktreeDeps, root, filter string, local bool, stale time.Duration) (ActiveReport, error) {
	return CollectActive(ctx, ActiveDependencies{Claims: deps.claims, Sessions: deps.sessions, Remote: deps.remote.domain()}, root, filter, local, stale)
}

type activeProvider struct {
	entries []remotestate.Entry
	err     error
}

func (provider activeProvider) Publish(context.Context, remotestate.Snapshot) (remotestate.PublishResult, error) {
	return remotestate.PublishResult{}, errors.New("unexpected publish")
}
func (provider activeProvider) List(context.Context) ([]remotestate.Entry, error) {
	return provider.entries, provider.err
}
func (provider activeProvider) Claim(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error) {
	return remotestate.ClaimOutcome{}, errors.New("unexpected claim")
}
func (provider activeProvider) Release(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
	return remotestate.ReleaseOutcome{}, errors.New("unexpected release")
}
func (provider activeProvider) Claims(context.Context) ([]remotestate.ClaimEntry, error) {
	return nil, errors.New("unexpected claims")
}

// cwWtFakeProvider embeds the remotestate.Provider interface so only List has
// to be implemented for the active-worktree preflight.
type cwWtFakeProvider struct {
	remotestate.Provider
	entries []remotestate.Entry
	err     error
	calls   int
}

func (provider *cwWtFakeProvider) List(context.Context) ([]remotestate.Entry, error) {
	provider.calls++
	return provider.entries, provider.err
}

func cwWtActiveDeps(claims func(string, string) ([]worktrees.ActiveClaimSummary, error),
	sessions func(string) ([]session.View, error),
	remote remoteDeps) activeWorktreeDeps {
	return activeWorktreeDeps{claims: claims, sessions: sessions, remote: remote}
}

func cwWtRemoteDeps(t *testing.T, login string, loginErr error, provider *cwWtFakeProvider, openErr error, now time.Time) remoteDeps {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  provider: git\n  repo: acme/state\n  machine: machine-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return remoteDeps{
		configPath: configPath,
		login:      func() (string, error) { return login, loginErr },
		open: func(remotestate.Config, string) (remotestate.Provider, error) {
			if openErr != nil {
				return nil, openErr
			}
			return provider, nil
		},
		now: func() time.Time { return now },
	}
}

func activeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("remote:\n  provider: git\n  repo: acme/state\n  machine: laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorktreeActiveCombinesLiveLocalAndOtherMachineSnapshots(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 12, 18, 0, 0, 0, time.UTC)
	deps := activeWorktreeDeps{
		claims: func(string, string) ([]worktrees.ActiveClaimSummary, error) {
			return []worktrees.ActiveClaimSummary{
				{Task: "local-task", TaskSummary: "Fix worktree discovery", Repository: "acme/widgets", Branch: "agent/local", Owner: "codex", WBSessionID: "wbs-live", RecordedAt: now},
				{Task: "recent-task", Repository: "acme/widgets", Branch: "agent/recent", Owner: "codex", WBSessionID: "wbs-gone", RecordedAt: now.Add(-time.Hour)},
				{Task: "old-task", Repository: "acme/widgets", Branch: "agent/old", Owner: "codex", WBSessionID: "wbs-gone", RecordedAt: now.Add(-48 * time.Hour)},
				{Task: "manual-task", Repository: "acme/widgets", Branch: "agent/manual", Owner: "alex", RecordedAt: now.Add(-48 * time.Hour)},
			}, nil
		},
		sessions: func(string) ([]session.View, error) {
			return []session.View{{Record: session.Record{WBSessionID: "wbs-live"}, State: session.StateLive}}, nil
		},
		remote: remoteDeps{
			configPath: activeConfig(t), login: func() (string, error) { return "alice", nil }, now: func() time.Time { return now },
			open: func(remotestate.Config, string) (remotestate.Provider, error) {
				return activeProvider{entries: []remotestate.Entry{
					{Snapshot: remotestate.Snapshot{Login: "alice", Machine: "laptop", PublishedAt: now.Add(-time.Hour), Worktrees: []remotestate.WorktreeState{{Task: "self", Repository: "acme/widgets", Branch: "agent/self", OwnerState: "active"}}}},
					{Snapshot: remotestate.Snapshot{Login: "alice", Machine: "laptop"}, Error: "/Users/alice/private/corrupt-snapshot"},
					{Snapshot: remotestate.Snapshot{Login: "alice", Machine: "vm", PublishedAt: now.Add(-48 * time.Hour), LastSeenAt: now, Worktrees: []remotestate.WorktreeState{
						{Task: "remote-task", TaskSummary: "Add task summaries", Repository: "acme/widgets", Branch: "agent/remote", OwnerState: "active", Lifecycle: "working", LastActivityAt: now.Add(-3 * time.Hour)},
						{Task: "orphaned", Repository: "acme/widgets", Branch: "agent/orphaned", OwnerState: "orphaned", Lifecycle: "working"},
						{Task: "finished", Repository: "acme/widgets", Branch: "agent/finished", OwnerState: "orphaned", Lifecycle: "merged"},
						{Task: "active-but-finished", Repository: "acme/widgets", Branch: "agent/active-finished", OwnerState: "active", Lifecycle: "merged"},
					}}},
				}}, nil
			},
		},
	}
	report, err := runWorktreeActive(context.Background(), deps, t.TempDir(), "acme/widgets", false, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Local.Status != "incomplete" || report.Local.OmittedUnresolvedClaims != 2 || report.Remote.Status != "stale" || report.Remote.Machines != 1 || report.Remote.StaleMachines != 1 || len(report.Remote.Snapshots) != 1 || len(report.Worktrees) != 3 {
		t.Fatalf("report = %+v", report)
	}
	if report.Remote.Snapshots[0].Machine != "alice/vm" || !report.Remote.Snapshots[0].Stale {
		t.Fatalf("remote snapshots = %+v", report.Remote.Snapshots)
	}
	if report.Remote.Snapshots[0].WorktreesPublished == report.Remote.Snapshots[0].MachineHeartbeat {
		t.Fatalf("claim heartbeat hid stale worktree publication: %+v", report.Remote.Snapshots[0])
	}
	if report.Worktrees[0].Task != "local-task" || report.Worktrees[0].Summary != "Fix worktree discovery" || report.Worktrees[1].Task != "recent-task" || report.Worktrees[1].OwnerState != "recent_claim" || report.Worktrees[2].Task != "remote-task" {
		t.Fatalf("rows = %+v", report.Worktrees)
	}
	remote := report.Worktrees[2]
	if remote.Locality != "remote" || remote.Machine != "alice/vm" || remote.Summary != "Add task summaries" || !remote.SnapshotStale {
		t.Fatalf("remote row = %+v", remote)
	}
}

func TestWorktreeActiveMakesRemoteFailureExplicitAndSupportsLocalOnly(t *testing.T) {
	t.Parallel()
	deps := activeWorktreeDeps{
		claims:   func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		sessions: func(string) ([]session.View, error) { return nil, nil },
		remote: remoteDeps{configPath: activeConfig(t), login: func() (string, error) { return "alice", nil }, now: time.Now,
			open: func(remotestate.Config, string) (remotestate.Provider, error) {
				return activeProvider{err: errors.New("/Users/alice/private/offline")}, nil
			}},
	}
	report, err := runWorktreeActive(context.Background(), deps, t.TempDir(), "", false, time.Hour)
	if err != nil || report.Remote.Status != "unavailable" || report.Remote.Error == "" || strings.Contains(report.Remote.Error, "/Users/") {
		t.Fatalf("remote failure report=%+v err=%v", report, err)
	}
	report, err = runWorktreeActive(context.Background(), deps, t.TempDir(), "", true, time.Hour)
	if err != nil || report.Remote.Status != "local_only" {
		t.Fatalf("local only report=%+v err=%v", report, err)
	}
}

func TestWorktreeActiveOmitsUnsafeRemoteSummaryAndReportsFinding(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 12, 18, 0, 0, 0, time.UTC)
	deps := activeWorktreeDeps{
		claims:   func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		sessions: func(string) ([]session.View, error) { return nil, nil },
		remote: remoteDeps{
			configPath: activeConfig(t), login: func() (string, error) { return "alice", nil }, now: func() time.Time { return now },
			open: func(remotestate.Config, string) (remotestate.Provider, error) {
				return activeProvider{entries: []remotestate.Entry{{Snapshot: remotestate.Snapshot{
					Login: "alice", Machine: "vm", PublishedAt: now,
					Worktrees: []remotestate.WorktreeState{
						{Task: "unsafe-control", TaskSummary: "line one\nline two", Repository: "acme/widgets", Branch: "unsafe-control", OwnerState: "active"},
						{Task: "unsafe-long", TaskSummary: strings.Repeat("x", worktrees.MaxTaskSummaryRunes+1), Repository: "acme/widgets", Branch: "unsafe-long", OwnerState: "active"},
					},
				}}}}, nil
			},
		},
	}
	report, err := runWorktreeActive(context.Background(), deps, t.TempDir(), "acme/widgets", false, 24*time.Hour)
	if err != nil || report.Remote.Status != "unavailable" || len(report.Worktrees) != 2 || report.Worktrees[0].Summary != "" || report.Worktrees[1].Summary != "" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
func TestOriginalRunWorktreeActiveLocalPaths(t *testing.T) {
	t.Parallel()
	now := time.Date(2025, 5, 5, 12, 0, 0, 0, time.UTC)
	live := []worktrees.ActiveClaimSummary{
		{Task: "zeta", Repository: "acme/app", Branch: "b1", WBSessionID: "wbs-1", RecordedAt: now.Add(-time.Hour)},
		{Task: "alpha", Repository: "acme/app", Branch: "b2", RecordedAt: now.Add(-2 * time.Hour)},
		{Task: "old", Repository: "acme/app", WBSessionID: "wbs-gone", RecordedAt: now.Add(-72 * time.Hour)},
	}
	deps := cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return live, nil },
		func(string) ([]session.View, error) {
			return []session.View{{Record: session.Record{PID: 1, WBSessionID: "wbs-1"}, State: session.StateLive}}, nil
		},
		cwWtRemoteDeps(t, "me", nil, &cwWtFakeProvider{}, nil, now),
	)
	report, err := runWorktreeActive(context.Background(), deps, "/tmp/projects", "", true, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Local.Status != "incomplete" || report.Local.OmittedUnresolvedClaims != 1 {
		t.Fatalf("local status = %+v", report.Local)
	}
	if report.Remote.Status != "local_only" || report.Remote.StaleAfter != "24h0m0s" {
		t.Fatalf("remote status = %+v", report.Remote)
	}
	if len(report.Worktrees) != 2 {
		t.Fatalf("rows = %+v", report.Worktrees)
	}
	if report.Worktrees[0].Task != "alpha" || report.Worktrees[0].OwnerState != "recent_claim" {
		t.Fatalf("sorted rows = %+v", report.Worktrees)
	}
	if report.Worktrees[1].Task != "zeta" || report.Worktrees[1].OwnerState != "active" {
		t.Fatalf("sorted rows = %+v", report.Worktrees)
	}

	// A zero stale window formats as empty and still short-circuits to local.
	report, err = runWorktreeActive(context.Background(), deps, "/tmp/projects", "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.StaleAfter != "" {
		t.Fatalf("zero stale window = %q", report.Remote.StaleAfter)
	}
}

func TestOriginalRunWorktreeActiveErrors(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	remote := cwWtRemoteDeps(t, "me", nil, &cwWtFakeProvider{}, nil, now)

	_, err := runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, errors.New("boom") },
		func(string) ([]session.View, error) { return nil, nil }, remote), "/tmp", "", true, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "local worktree inventory unavailable") {
		t.Fatalf("claims error = %v", err)
	}

	_, err = runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, errors.New("boom") }, remote), "/tmp", "", true, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "local session inventory unavailable") {
		t.Fatalf("sessions error = %v", err)
	}

	// Remote configuration unavailable.
	badConfig := remote
	badConfig.configPath = filepath.Join(t.TempDir(), "missing.yaml")
	report, err := runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, nil }, badConfig), "/tmp", "", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.Status != "unavailable" || !strings.Contains(report.Remote.Error, "remote configuration unavailable") {
		t.Fatalf("remote config error report = %+v", report.Remote)
	}

	// Local machine identity unavailable.
	noLogin := remote
	noLogin.login = func() (string, error) { return "", errors.New("no login") }
	report, err = runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, nil }, noLogin), "/tmp", "", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.Status != "unavailable" || !strings.Contains(report.Remote.Error, "local machine identity unavailable") {
		t.Fatalf("login error report = %+v", report.Remote)
	}

	// Provider list failure, and provider construction failure.
	failing := &cwWtFakeProvider{err: errors.New("list down")}
	report, err = runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, nil },
		cwWtRemoteDeps(t, "me", nil, failing, nil, now)), "/tmp", "", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.Status != "unavailable" || !strings.Contains(report.Remote.Error, "remote snapshots unavailable") {
		t.Fatalf("list error report = %+v", report.Remote)
	}

	report, err = runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, nil },
		cwWtRemoteDeps(t, "me", nil, nil, errors.New("open failed"), now)), "/tmp", "", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.Status != "unavailable" {
		t.Fatalf("open error report = %+v", report.Remote)
	}
}

func TestOriginalRunWorktreeActiveRemoteSnapshots(t *testing.T) {
	t.Parallel()
	now := time.Date(2025, 5, 5, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-48 * time.Hour)
	fresh := now.Add(-time.Hour)
	provider := &cwWtFakeProvider{entries: []remotestate.Entry{
		// The local machine's own old publication is skipped.
		{Snapshot: remotestate.Snapshot{Login: "me", Machine: "machine-a", PublishedAt: fresh}},
		// A snapshot that could not be decoded.
		{Error: "decode failed"},
		// A stale machine carrying one includable and one excluded worktree,
		// plus one whose summary is invalid.
		{Snapshot: remotestate.Snapshot{
			Login: "them", Machine: "machine-b", PublishedAt: stale,
			Worktrees: []remotestate.WorktreeState{
				{Task: "wanted", Repository: "acme/app", Branch: "b", OwnerState: "active", Lifecycle: "working", TaskSummary: "fine", LastActivityAt: fresh},
				{Task: "terminal", Repository: "acme/app", Branch: "b", OwnerState: "active", Lifecycle: "merged"},
				{Task: "idle", Repository: "acme/app", Branch: "b", OwnerState: "orphaned", Lifecycle: "working"},
				{Task: "other-repo", Repository: "acme/elsewhere", Branch: "b", OwnerState: "active", Lifecycle: "working"},
				{Task: "bad-summary", Repository: "acme/app", Branch: "b", OwnerState: "active", Lifecycle: "working", TaskSummary: "two\nlines"},
			},
		}},
		// A fresh machine with no worktrees.
		{Snapshot: remotestate.Snapshot{Login: "them", Machine: "machine-c", PublishedAt: fresh}},
	}}
	deps := cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, nil },
		cwWtRemoteDeps(t, "me", nil, provider, nil, now),
	)
	report, err := runWorktreeActive(context.Background(), deps, "/tmp", "acme/app", false, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.Status != "unavailable" {
		t.Fatalf("an undecodable snapshot must mark the remote unavailable: %+v", report.Remote)
	}
	if !strings.Contains(report.Remote.Error, "could not be decoded") && !strings.Contains(report.Remote.Error, "invalid task summaries") {
		t.Fatalf("remote error = %q", report.Remote.Error)
	}
	if report.Remote.Machines != 2 {
		t.Fatalf("machines = %d, want 2 (the undecodable entry contributes none)", report.Remote.Machines)
	}
	if report.Remote.StaleMachines != 1 {
		t.Fatalf("stale machines = %d, want 1", report.Remote.StaleMachines)
	}
	if len(report.Remote.Snapshots) != 2 || report.Remote.Snapshots[0].Machine != "them/machine-b" {
		t.Fatalf("snapshots = %+v", report.Remote.Snapshots)
	}
	if len(report.Worktrees) != 2 {
		t.Fatalf("included rows = %+v", report.Worktrees)
	}
	for _, row := range report.Worktrees {
		if row.Locality != "remote" || !row.SnapshotStale || row.Machine != "them/machine-b" {
			t.Fatalf("row = %+v", row)
		}
	}
}

func TestOriginalActiveHelpers(t *testing.T) {
	t.Parallel()
	if includeRemoteActive(remotestate.WorktreeState{OwnerState: "active", Lifecycle: "working"}) != true {
		t.Fatal("an active working remote worktree must be included")
	}
	if includeRemoteActive(remotestate.WorktreeState{OwnerState: "active", Lifecycle: "MERGED "}) != false {
		t.Fatal("a terminal lifecycle must be excluded")
	}
	if includeRemoteActive(remotestate.WorktreeState{OwnerState: "orphaned", Lifecycle: "working"}) != false {
		t.Fatal("a non-active owner must be excluded")
	}
	for _, value := range []string{"merged", "Superseded", "terminal", "removed", "discarded", "  MERGED  "} {
		if !terminalRemoteLifecycle(value) {
			t.Errorf("terminalRemoteLifecycle(%q) = false", value)
		}
	}
	if terminalRemoteLifecycle("working") {
		t.Fatal("working must not be terminal")
	}

	views := []session.View{
		{Record: session.Record{WBSessionID: "live-1", PID: 1}, State: session.StateLive},
		{Record: session.Record{WBSessionID: "gone-1", PID: 2}, State: session.StateGone},
		{Record: session.Record{WBSessionID: "", PID: 3}, State: session.StateLive},
	}
	ids := activeSessionIDs(views)
	if !ids["live-1"] || ids["gone-1"] || len(ids) != 1 {
		t.Fatalf("active session ids = %v", ids)
	}

	now := time.Now().UTC()
	if state, include := localClaimOwnerState(worktrees.ActiveClaimSummary{WBSessionID: "live-1"}, ids, now); !include || state != "active" {
		t.Fatalf("live claim = (%q, %t)", state, include)
	}
	if state, include := localClaimOwnerState(worktrees.ActiveClaimSummary{RecordedAt: now.Add(-time.Hour)}, ids, now); !include || state != "recent_claim" {
		t.Fatalf("recent claim = (%q, %t)", state, include)
	}
	if _, include := localClaimOwnerState(worktrees.ActiveClaimSummary{RecordedAt: now.Add(-48 * time.Hour)}, ids, now); include {
		t.Fatal("an old unresolved claim must be omitted")
	}
	if _, include := localClaimOwnerState(worktrees.ActiveClaimSummary{}, ids, now); include {
		t.Fatal("a claim with no time and no live session must be omitted")
	}

	if formatActiveTime(time.Time{}) != "" {
		t.Fatal("zero time must format as empty")
	}
	if got := formatActiveTime(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)); got != "2025-01-02T03:04:05Z" {
		t.Fatalf("formatActiveTime = %q", got)
	}
	if formatActiveStaleAfter(0) != "" || formatActiveStaleAfter(-time.Second) != "" {
		t.Fatal("non-positive stale windows must format as empty")
	}
	if got := formatActiveStaleAfter(90 * time.Second); got != "1m30s" {
		t.Fatalf("formatActiveStaleAfter = %q", got)
	}
	if normalizedOwnerState("  ") != "unknown" || normalizedOwnerState("active") != "active" {
		t.Fatal("normalizedOwnerState")
	}
	if normalizedLifecycle("  ") != "working" || normalizedLifecycle("merged") != "merged" {
		t.Fatal("normalizedLifecycle")
	}
	if !matchesWorktreeFilter("acme/app", "") {
		t.Fatal("empty filter must match everything")
	}
	if !matchesWorktreeFilter("Acme/App", "acme") || matchesWorktreeFilter("acme/app", "other") {
		t.Fatal("matchesWorktreeFilter")
	}

	// The final tie-break is the branch, reached only when repository, task,
	// and machine are equal.
	rows := []ActiveRow{
		{Repository: "acme/app", Task: "t", Machine: "m", Branch: "z"},
		{Repository: "acme/app", Task: "t", Machine: "m", Branch: "a"},
	}
	sortActiveWorktrees(rows)
	if rows[0].Branch != "a" || rows[1].Branch != "z" {
		t.Fatalf("branch tie-break = %+v", rows)
	}
}
