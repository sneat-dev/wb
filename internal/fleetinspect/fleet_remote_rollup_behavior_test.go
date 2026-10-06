package fleetinspect

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestFleetRemoteRollupCountsEachReconciliationDisposition(t *testing.T) {
	t.Parallel()
	service := testService()

	previousRepositories, previousSync := service.deps.Remote, service.deps.Sync
	t.Cleanup(func() { service.deps.Remote, service.deps.Sync = previousRepositories, previousSync })
	repositories := []discover.Repo{
		{Org: "acme", Name: "clone", Remote: true},
		{Org: "acme", Name: "pull", Local: true, Remote: true},
		{Org: "acme", Name: "dirty", Local: true, Remote: true},
		{Org: "acme", Name: "ignored", Local: true, Remote: true},
		{Org: "acme", Name: "empty", Remote: true},
		{Org: "acme", Name: "archived", Local: true, Remote: true, Archived: true},
		{Org: "acme", Name: "current", Local: true, Remote: true},
		{Org: "acme", Name: "broken", Local: true, Remote: true},
		{Org: "acme", Name: "local", Local: true},
		{Org: "acme", Name: "remote", Remote: true},
		{Org: "acme", Name: "archived-remote", Remote: true, Archived: true},
		{Org: "acme", Name: "fork", Remote: true, IsFork: true},
	}
	service.deps.Remote = func(_ string, _ string, _ func() []string) ([]discover.Repo, error) { return repositories, nil }
	statuses := map[string]fleetsync.Status{
		"clone": fleetsync.Cloned, "pull": fleetsync.Pulled, "dirty": fleetsync.SkippedDirty,
		"ignored": fleetsync.SkippedIgnored, "empty": fleetsync.EmptyRemote, "archived": fleetsync.ArchivedUnlandable,
		"current": fleetsync.NoOp, "broken": fleetsync.Failed,
		"local": fleetsync.Diverged, "remote": fleetsync.Diverged,
		"archived-remote": fleetsync.Diverged, "fork": fleetsync.Diverged,
	}
	var inspected []string
	service.deps.Sync = func(_ context.Context, Repository discover.Repo, _ string, dryRun, pruneArchived bool) fleetsync.Result {
		if !dryRun || !pruneArchived {
			t.Fatalf("remote rollup called sync with dry=%t prune=%t", dryRun, pruneArchived)
		}
		inspected = append(inspected, Repository.Name)
		return fleetsync.Result{Repo: Repository, Status: statuses[Repository.Name]}
	}
	stats, err := service.fleetRemoteRollup(t.TempDir(), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.WouldClone != 1 || stats.WouldPull != 1 || stats.SkippedDirty != 1 || stats.Ignored != 1 || stats.EmptyRemote != 1 || stats.ArchivedUnlandable != 1 || stats.NoOp != 1 || stats.Error != 1 {
		t.Fatalf("disposition counts = %+v", stats)
	}
	if stats.LocalOnly != 1 || stats.RemoteOnly != 3 {
		t.Fatalf("placement counts = %+v", stats)
	}
	if len(inspected) != len(repositories) {
		t.Fatalf("inspected = %v", inspected)
	}
}

func TestFleetRemoteRollupFiltersBeforeInspectingRepositories(t *testing.T) {
	t.Parallel()
	service := testService()

	previousRepositories, previousSync := service.deps.Remote, service.deps.Sync
	t.Cleanup(func() { service.deps.Remote, service.deps.Sync = previousRepositories, previousSync })
	service.deps.Remote = func(_ string, _ string, _ func() []string) ([]discover.Repo, error) {
		return []discover.Repo{
			{Org: "acme", Name: "orders-service", Remote: true},
			{Org: "acme", Name: "billing-tool", Remote: true},
			{Org: "other", Name: "orders-service", Remote: true},
		}, nil
	}
	var inspected []string
	service.deps.Sync = func(_ context.Context, Repository discover.Repo, _ string, _, _ bool) fleetsync.Result {
		inspected = append(inspected, Repository.Slug())
		return fleetsync.Result{Repo: Repository, Status: fleetsync.Cloned}
	}
	stats, err := service.fleetRemoteRollup(t.TempDir(), "acme", Options{Match: "acme/*", Regex: `-service$`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(inspected, ",") != "acme/orders-service" || stats.WouldClone != 1 || stats.RemoteOnly != 1 {
		t.Fatalf("inspected=%v stats=%+v", inspected, stats)
	}
	inspected = nil
	if _, err := service.fleetRemoteRollup(t.TempDir(), "", Options{Regex: `[`}); err == nil || !strings.Contains(err.Error(), "invalid --regex") {
		t.Fatalf("invalid pattern error = %v", err)
	}
	if len(inspected) != 0 {
		t.Fatalf("invalid pattern inspected repositories: %v", inspected)
	}
	want := errors.New("remote repository inventory unavailable")
	service.deps.Remote = func(string, string, func() []string) ([]discover.Repo, error) { return nil, want }
	if _, err := service.fleetRemoteRollup(t.TempDir(), "", Options{}); !errors.Is(err, want) {
		t.Fatalf("inventory error = %v", err)
	}
}

func TestFleetWorktreeRollupCountsFilteredTasksAndCheckoutState(t *testing.T) {
	t.Parallel()
	service := testService()

	previous := service.deps.Worktrees
	t.Cleanup(func() { service.deps.Worktrees = previous })
	service.deps.Worktrees = func(_ context.Context, options worktrees.ListOptions) ([]worktrees.ListResult, error) {
		if options.Filter != "acme" {
			t.Fatalf("worktree filter = %q", options.Filter)
		}
		return []worktrees.ListResult{
			{Task: "feature", Repository: "acme/orders-service", Clean: false, Locked: true},
			{Task: "feature", Repository: "acme/billing-service", Clean: true},
			{Task: "repair", Repository: "acme/data-service", Clean: false},
			{Task: "other", Repository: "other/orders-service", Clean: false, Locked: true},
			{Task: "tool", Repository: "acme/build-tool", Clean: false, Locked: true},
		}, nil
	}
	stats, err := service.fleetWorktreeRollup(t.TempDir(), "acme", Options{Match: "acme/*", Regex: `-service$`})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Tasks != 2 || stats.Checkouts != 3 || stats.Dirty != 2 || stats.Locked != 1 {
		t.Fatalf("worktree stats = %+v", stats)
	}
	if _, err := service.fleetWorktreeRollup(t.TempDir(), "acme", Options{Regex: `[`}); err == nil || !strings.Contains(err.Error(), "invalid --regex") {
		t.Fatalf("invalid pattern error = %v", err)
	}
	want := errors.New("managed checkout inventory unavailable")
	service.deps.Worktrees = func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) { return nil, want }
	if _, err := service.fleetWorktreeRollup(t.TempDir(), "acme", Options{}); !errors.Is(err, want) {
		t.Fatalf("inventory error = %v", err)
	}
}
