package githubapp

import (
	"context"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

// denyAccess denies exactly the machines named in deny, allowing everything
// else. Used to drive project()'s "viewer may not see this machine" branch.
type denyAccess struct{ deny map[string]bool }

func (access denyAccess) CanViewMachine(_ context.Context, _ Viewer, _ string, machine string) (bool, error) {
	return !access.deny[machine], nil
}

// TestProjectAppliesDefaultStaleAfterWhenUnset drives RemoteStateReadModel.
// project's `staleAfter <= 0` branch: a model with no StaleAfter configured
// must fall back to DefaultMachineStaleAfter rather than treating every
// machine as immediately stale.
func TestProjectAppliesDefaultStaleAfterWhenUnset(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	publishedAt := now.Add(-2 * time.Hour) // stale under a 1h threshold, not under the 24h default.
	model := RemoteStateReadModel{
		Store:  fixedSnapshots{records: []machinesnapshot.StoredSnapshot{stored(testSnapshot(publishedAt), publishedAt)}},
		Access: localMachineAccess{},
		Now:    func() time.Time { return now },
		// StaleAfter intentionally left zero.
	}
	access, err := model.Dashboard(context.Background(), localViewer())
	if err != nil {
		t.Fatalf("Dashboard() error = %v", err)
	}
	machine := access.Value.Fleet.Machines[0]
	if machine.Stale || machine.State != "online" {
		t.Fatalf("machine = %+v; want not stale under the default 24h threshold", machine)
	}
}

// TestProjectSkipsMachinesTheViewerMayNotSee drives the `!allowed` branch: a
// record the access resolver denies must be left out of the fleet entirely,
// not merely hidden from a field.
func TestProjectSkipsMachinesTheViewerMayNotSee(t *testing.T) {
	t.Parallel()
	publishedAt := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)
	visible := testSnapshot(publishedAt)
	visible.Machine = "visible"
	hidden := testSnapshot(publishedAt)
	hidden.Machine = "hidden"

	model := RemoteStateReadModel{
		Store: fixedSnapshots{records: []machinesnapshot.StoredSnapshot{
			stored(visible, publishedAt), stored(hidden, publishedAt),
		}},
		Access:     denyAccess{deny: map[string]bool{"hidden": true}},
		Now:        func() time.Time { return publishedAt },
		StaleAfter: time.Hour,
	}
	access, err := model.Dashboard(context.Background(), localViewer())
	if err != nil {
		t.Fatalf("Dashboard() error = %v", err)
	}
	if len(access.Value.Fleet.Machines) != 1 || access.Value.Fleet.Machines[0].Name != "visible" {
		t.Fatalf("machines = %+v; want only the visible machine", access.Value.Fleet.Machines)
	}
}

// TestProjectSortsMachinesByName drives the sort.Slice comparator over
// machines: records published out of name order must come back sorted.
func TestProjectSortsMachinesByName(t *testing.T) {
	t.Parallel()
	publishedAt := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)
	zed := testSnapshot(publishedAt)
	zed.Machine = "zed"
	alpha := testSnapshot(publishedAt)
	alpha.Machine = "alpha"

	model := RemoteStateReadModel{
		Store: fixedSnapshots{records: []machinesnapshot.StoredSnapshot{
			stored(zed, publishedAt), stored(alpha, publishedAt),
		}},
		Access:     localMachineAccess{},
		Now:        func() time.Time { return publishedAt },
		StaleAfter: time.Hour,
	}
	access, err := model.Dashboard(context.Background(), localViewer())
	if err != nil {
		t.Fatalf("Dashboard() error = %v", err)
	}
	machines := access.Value.Fleet.Machines
	if len(machines) != 2 || machines[0].Name != "alpha" || machines[1].Name != "zed" {
		t.Fatalf("machines = %+v; want alpha before zed", machines)
	}
}

// TestMachineMarksStaleByHeartbeatAge drives RemoteStateReadModel.machine's
// `stale` branch directly: a heartbeat older than staleAfter must be
// reported stale, and one within the window must not.
func TestMachineMarksStaleByHeartbeatAge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	model := RemoteStateReadModel{}
	staleSnapshot := testSnapshot(now.Add(-2 * time.Hour))
	fresh := model.machine(testSnapshot(now.Add(-10*time.Minute)), now.Add(-10*time.Minute), scopeFilter{}, map[string]bool{}, now, time.Hour)
	if fresh.Stale || fresh.State != "online" {
		t.Fatalf("fresh machine = %+v; want online", fresh)
	}
	stale := model.machine(staleSnapshot, now.Add(-2*time.Hour), scopeFilter{}, map[string]bool{}, now, time.Hour)
	if !stale.Stale || stale.State != "stale" {
		t.Fatalf("stale machine = %+v; want stale", stale)
	}
}

// TestMachineSortsWorktreesByRepositoryThenTask drives the worktree sort
// comparator's tie-break: same repository must fall back to Task order.
func TestMachineSortsWorktreesByRepositoryThenTask(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	snapshot := testSnapshot(now,
		machinesnapshot.Worktree{Task: "zeta", Repository: "acme/widgets", Branch: "feat/z"},
		machinesnapshot.Worktree{Task: "alpha", Repository: "acme/widgets", Branch: "feat/a"},
	)
	model := RemoteStateReadModel{}
	machine := model.machine(snapshot, now, scopeFilter{}, map[string]bool{}, now, time.Hour)
	if len(machine.Worktrees) != 2 || machine.Worktrees[0].Task != "alpha" || machine.Worktrees[1].Task != "zeta" {
		t.Fatalf("worktrees = %+v; want alpha before zeta within the same repository", machine.Worktrees)
	}
}

// TestScopeFilterMatchesMachineWithNoScope drives matchesMachine's
// short-circuit: an empty scope or id means "match everything".
func TestScopeFilterMatchesMachineWithNoScope(t *testing.T) {
	t.Parallel()
	snapshot := testSnapshot(time.Now())
	if !(scopeFilter{}).matchesMachine(snapshot) {
		t.Fatal("empty scope filter must match every machine")
	}
}

// TestScopeFilterMatchesRepositoryRejectsUnknownScope drives
// matchesRepository's default case: an unrecognised scope value matches
// nothing, rather than falling open.
func TestScopeFilterMatchesRepositoryRejectsUnknownScope(t *testing.T) {
	t.Parallel()
	filter := scopeFilter{scope: Scope("bogus"), id: "acme"}
	if filter.matchesRepository("github.com/acme/widgets") {
		t.Fatal("unrecognised scope must not match any repository")
	}
}

// TestFleetWorktreeRejectsMissingRequiredFields drives fleetWorktree's
// false-return branch for a worktree missing a required field.
func TestFleetWorktreeRejectsMissingRequiredFields(t *testing.T) {
	t.Parallel()
	if _, ok := fleetWorktree(machinesnapshot.Worktree{Repository: "acme/widgets", Branch: "feat/a"}); ok {
		t.Fatal("fleetWorktree with no task must be rejected")
	}
	if _, ok := fleetWorktree(machinesnapshot.Worktree{Task: "t", Branch: "feat/a"}); ok {
		t.Fatal("fleetWorktree with no repository must be rejected")
	}
	if _, ok := fleetWorktree(machinesnapshot.Worktree{Task: "t", Repository: "acme/widgets"}); ok {
		t.Fatal("fleetWorktree with no branch must be rejected")
	}
}

// TestFleetWorktreeCarriesStream drives the stream-assignment branch: a
// non-empty stream must survive onto the row.
func TestFleetWorktreeCarriesStream(t *testing.T) {
	t.Parallel()
	row, ok := fleetWorktree(machinesnapshot.Worktree{
		Task: "t", Repository: "acme/widgets", Branch: "feat/a", Stream: "release-9",
	})
	if !ok {
		t.Fatal("fleetWorktree with all required fields must be accepted")
	}
	if row.Stream != "release-9" {
		t.Fatalf("row.Stream = %q; want release-9", row.Stream)
	}
}

// TestCanonicalWorktreeRepositoryLeavesEmptyAndPrefixedAlone drives the
// early-return branch: blank input and an already-canonical repository come
// back unchanged rather than double-prefixed.
func TestCanonicalWorktreeRepositoryLeavesEmptyAndPrefixedAlone(t *testing.T) {
	t.Parallel()
	if got := canonicalWorktreeRepository(""); got != "" {
		t.Fatalf("canonicalWorktreeRepository(\"\") = %q; want empty", got)
	}
	if got := canonicalWorktreeRepository("github.com/acme/widgets"); got != "github.com/acme/widgets" {
		t.Fatalf("canonicalWorktreeRepository(already canonical) = %q; want unchanged", got)
	}
}

// TestFleetPullRequestRejectsMissingOrNonPositiveNumber drives
// fleetPullRequest's nil-return branch.
func TestFleetPullRequestRejectsMissingOrNonPositiveNumber(t *testing.T) {
	t.Parallel()
	if got := fleetPullRequest(nil); got != nil {
		t.Fatalf("fleetPullRequest(nil) = %+v; want nil", got)
	}
	if got := fleetPullRequest(&machinesnapshot.PullRequest{Number: 0, URL: "https://github.com/a/b/pull/1", State: "open"}); got != nil {
		t.Fatalf("fleetPullRequest(Number: 0) = %+v; want nil", got)
	}
}
