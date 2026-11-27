package fleet_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/deskpublish"
)

// TestDeskReductionWithholdsNamesAtRepositoriesDetail runs the sentinel export
// through the Desk reduction (desk-connect#ac:detail-level-enforced-locally):
// at `repositories` no task, stream, branch other than the default, worktree,
// agent-task, failing-check or slowest-task name, and no unkeyed worktree id,
// leaves the machine; at `full` the names are there; at neither level is
// anything outside the metadata set.
func TestDeskReductionWithholdsNamesAtRepositoriesDetail(t *testing.T) {
	t.Parallel()
	envelope := fleet.SentinelExport(t)
	document := envelope.Fleet
	if document == nil || len(document.Worktrees) == 0 || document.Worktrees[0].Task == "" || document.Worktrees[0].Branch == "" {
		t.Fatalf("the sentinel export has no named worktree to withhold: %+v", document)
	}
	throughput := &fleet.Throughput{WindowDays: 7, Slowest: []fleet.ThroughputTask{
		{Task: "slowest-task-name", DurationSeconds: 90, LandedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}}}
	withheld := map[string]bool{"slowest-task-name": true}
	defaults := map[string]bool{}
	for _, repository := range document.Repositories {
		defaults[repository.DefaultBranch] = true
	}
	for _, worktree := range document.Worktrees {
		for _, value := range []string{worktree.ID, worktree.Name, worktree.Task, worktree.Stream} {
			withheld[value] = true
		}
		if !defaults[worktree.Branch] {
			withheld[worktree.Branch] = true
		}
	}
	for _, agent := range document.Agents {
		withheld[agent.Task] = true
	}
	for _, pull := range document.PullRequests {
		withheld[pull.FailedCheck] = true
		if !defaults[pull.Branch] {
			withheld[pull.Branch] = true
		}
	}
	delete(withheld, "")

	key, err := deskpublish.NewTaskKey(strings.NewReader(strings.Repeat("k", deskpublish.TaskKeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	reduced, err := deskpublish.Reduce(envelope, throughput, desk.LevelRepositories, key)
	if err != nil {
		t.Fatal(err)
	}
	body := string(reduced.Envelope)
	for value := range withheld {
		quoted, _ := json.Marshal(value)
		if strings.Contains(body, string(quoted)) {
			t.Errorf("repositories detail carries withheld value %s", quoted)
		}
	}
	for _, forbidden := range []string{fleet.Sentinel, "machine_routes", "publish_error", "/Users/"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("repositories detail carries %q", forbidden)
		}
	}
	if want := key.Of(document.Worktrees[0].Task); !strings.Contains(body, `"task_key":"`+want+`"`) {
		t.Errorf("repositories detail lacks the worktree's task key %s", want)
	}
	if !strings.Contains(body, `"name":"acme/widgets"`) || !strings.Contains(body, `"window_days":7`) {
		t.Errorf("repositories detail must keep repository identities and throughput counts: %s", body)
	}
	if err := reduced.Request(time.Now()).Validate(); err != nil {
		t.Errorf("the publish request is not valid: %v", err)
	}

	full, err := deskpublish.Reduce(envelope, throughput, desk.LevelFull, deskpublish.TaskKey{})
	if err != nil {
		t.Fatal(err)
	}
	fullBody := string(full.Envelope)
	for _, kept := range []string{document.Worktrees[0].Task, document.Worktrees[0].Branch, "slowest-task-name"} {
		if !strings.Contains(fullBody, kept) {
			t.Errorf("full detail lacks %q", kept)
		}
	}
	for _, forbidden := range []string{fleet.Sentinel, "machine_routes", "publish_error", "/Users/", "task_key"} {
		if strings.Contains(fullBody, forbidden) {
			t.Errorf("full detail carries %q", forbidden)
		}
	}
}
