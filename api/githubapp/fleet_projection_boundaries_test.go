package githubapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

type unavailableMachineAccess struct{ failure error }

func (access unavailableMachineAccess) CanViewMachine(context.Context, Viewer, string, string) (bool, error) {
	return false, access.failure
}

func TestStatsPropagatesMachineAuthorizationFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)
	failure := errors.New("machine authorization unavailable")
	model := readModel(stored(testSnapshot(now), now))
	model.Access = unavailableMachineAccess{failure: failure}
	if access, err := model.Stats(context.Background(), localViewer(), ScopeRepository, "github.com/acme/widgets"); !errors.Is(err, failure) || access.Value.Fleet != nil {
		t.Fatalf("stats = %+v, %v", access, err)
	}
}

func TestMachineProjectionSkipsUnmatchedAndIncompleteWorktrees(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)
	snapshot := testSnapshot(now,
		machinesnapshot.Worktree{Task: "other", Repository: "acme/gadgets", Branch: "main"},
		machinesnapshot.Worktree{Task: "missing-branch", Repository: "acme/widgets"},
		machinesnapshot.Worktree{Task: "visible", Repository: "acme/widgets", Branch: "feature", AttentionReason: "manual review required"},
	)
	repos := map[string]bool{}
	machine := (RemoteStateReadModel{}).machine(snapshot, now, scopeFilter{scope: ScopeRepository, id: "github.com/acme/widgets"}, repos, now, time.Hour)
	if len(machine.Worktrees) != 1 || machine.Worktrees[0].Task != "visible" || !machine.Worktrees[0].NeedsAttention || machine.Worktrees[0].AttentionReason != "worktree requires attention" {
		t.Fatalf("worktrees = %+v", machine.Worktrees)
	}
	if len(repos) != 1 || !repos["github.com/acme/widgets"] {
		t.Fatalf("repositories = %v", repos)
	}
}

func TestFleetPullRequestDropsUnknownState(t *testing.T) {
	t.Parallel()
	request := &machinesnapshot.PullRequest{Number: 7, URL: "https://github.com/acme/widgets/pull/7", State: "unexpected"}
	if got := fleetPullRequest(request); got != nil {
		t.Fatalf("unknown state reached fleet: %+v", got)
	}
}

func TestFleetPullRequestRejectsInvalidURLs(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"%", "/acme/widgets/pull/7", "file://github.com/acme/widgets/pull/7"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			request := &machinesnapshot.PullRequest{Number: 7, URL: raw, State: "open"}
			if got := fleetPullRequest(request); got != nil {
				t.Fatalf("invalid link reached fleet: %+v", got)
			}
		})
	}
}
