package fleetinspect

import (
	"context"
	"errors"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/layout"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/repostatus"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestCollectionOrdersFailuresAndRetainsDepthAndAttention(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"select", "scan", "worktrees", "layout", "remote", "success"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			service := testService()
			sentinel := errors.New(stage)
			calls := []string{}
			service.deps.Select = func(request reposelection.Request) ([]reposelection.Target, error) {
				calls = append(calls, "select")
				if request.ProjectsRoot != "projects" || !request.Fleet {
					t.Fatal(request)
				}
				if stage == "select" {
					return nil, sentinel
				}
				return []reposelection.Target{{Repository: "acme/app", Path: "app"}}, nil
			}
			service.deps.Inspect = func([]reposelection.Target, int, func(reposelection.Target, repostatus.Row)) []repostatus.Row {
				return []repostatus.Row{{Repository: "acme/app", Status: "clean"}, {Repository: "acme/dirty", Status: "attention"}}
			}
			service.deps.Scan = func(string) ([]discover.Repo, error) {
				calls = append(calls, "scan")
				if stage == "scan" {
					return nil, sentinel
				}
				return []discover.Repo{{Org: "acme", Name: "app"}}, nil
			}
			service.deps.Worktrees = func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
				calls = append(calls, "worktrees")
				if stage == "worktrees" {
					return nil, sentinel
				}
				return nil, nil
			}
			service.deps.Layout = func(context.Context, string) (layout.Summary, error) {
				calls = append(calls, "layout")
				if stage == "layout" {
					return layout.Summary{}, sentinel
				}
				return layout.Summary{}, nil
			}
			service.deps.Remote = func(_, _ string, owners func() []string) ([]discover.Repo, error) {
				calls = append(calls, "remote")
				owners()
				if stage == "remote" {
					return nil, sentinel
				}
				return nil, nil
			}
			service.deps.Owners = func([]string) []string { return nil }
			service.deps.Hooks = func(string, string, string, string) (report hooks.CheckReport, err error) { return }
			request := Request{ProjectsRoot: "projects", Parallel: 1, Remote: true, Hooks: true}
			report, err := service.Overview(t.Context(), request)
			if stage != "success" {
				if !errors.Is(err, sentinel) || len(report.Status.Repositories) != 0 {
					t.Fatal(calls, report, err)
				}
				return
			}
			if err != nil || report.Stats.Remote == nil || report.Stats.Hooks == nil || len(report.Status.Repositories) != 1 {
				t.Fatal(report, err)
			}
			request.All = true
			if report, err = service.Overview(t.Context(), request); err != nil || len(report.Status.Repositories) != 2 {
				t.Fatal(report, err)
			}
			if stats, err := service.Stats(t.Context(), request); err != nil || stats.Git.Clean != 1 {
				t.Fatal(stats, err)
			}
		})
	}
}
func TestInventoryScanFailurePreservesIdentity(t *testing.T) {
	t.Parallel()
	service := testService()
	sentinel := errors.New("scan")
	service.deps.Scan = func(string) ([]discover.Repo, error) { return nil, sentinel }
	if _, err := service.fleetInventory("projects", "", Options{}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
