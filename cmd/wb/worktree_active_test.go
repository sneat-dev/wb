package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

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

func activeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("remote:\n  provider: git\n  repo: acme/state\n  machine: laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorktreeActiveCombinesLiveLocalAndOtherMachineSnapshots(t *testing.T) {
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

func TestWorktreeActiveOmitsUnsafeRemoteSummaryAndReportsFinding(t *testing.T) {
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
	var textOutput bytes.Buffer
	if err := writeActiveWorktreeText(&textOutput, report); err != nil || strings.Contains(textOutput.String(), "line one") || strings.Contains(textOutput.String(), "line two") || strings.Contains(textOutput.String(), strings.Repeat("x", worktrees.MaxTaskSummaryRunes+1)) {
		t.Fatalf("unsafe text output=%q err=%v", textOutput.String(), err)
	}
}

func TestWorktreeActiveMakesRemoteFailureExplicitAndSupportsLocalOnly(t *testing.T) {
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

func TestWorktreeActiveCommandWritesStaleReportThenReturnsFinding(t *testing.T) {
	now := time.Date(2026, 9, 12, 18, 0, 0, 0, time.UTC)
	deps := activeWorktreeDeps{
		claims:   func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		sessions: func(string) ([]session.View, error) { return nil, nil },
		remote: remoteDeps{configPath: activeConfig(t), login: func() (string, error) { return "alice", nil }, now: func() time.Time { return now },
			open: func(remotestate.Config, string) (remotestate.Provider, error) {
				return activeProvider{entries: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "alice", Machine: "vm", PublishedAt: now.Add(-48 * time.Hour)}}}}, nil
			}},
	}
	oldProjectsRoot, oldFilter := projectsRoot, filterFlag
	projectsRoot, filterFlag = t.TempDir(), ""
	t.Cleanup(func() { projectsRoot, filterFlag = oldProjectsRoot, oldFilter })
	command := newWorktreeActiveCmdWithDeps(deps)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"--format", "json"})
	err := command.Execute()
	var finding *exitError
	if !errors.As(err, &finding) || finding.code != exitFindings || !strings.Contains(output.String(), `"status":"stale"`) {
		t.Fatalf("err=%v output=%s", err, output.String())
	}
}
