package worktrees

import (
	"context"
	"testing"
)

// TestListRegisteredPullRequestBindingsReadsOnlyRecordedBindings pins the
// daemon-watches-only-registered-prs AC: the reader must return exactly the
// pull requests RecordClaimPullRequestBinding recorded, and nothing for an
// active claim that never opened one — herdr-session-transport's watcher
// (Plan Task 6) has no fleet-wide scan to fall back on, so an unbound claim
// here would otherwise silently be watched too.
func TestListRegisteredPullRequestBindingsReadsOnlyRecordedBindings(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "pr-binding-bound",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	bound := created[0]

	// A second, unbound claim in the same home: never given a binding.
	createdUnbound, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "pr-binding-unbound",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	unbound := createdUnbound[0]

	task, claimID, err := RecordClaimPullRequestBinding(fixture.projectsRoot, bound.WorktreeDir, ClaimPullRequestBinding{
		Repository: "acme/app", PullRequest: 42, URL: "https://github.com/acme/app/pull/42",
	})
	if err != nil {
		t.Fatalf("RecordClaimPullRequestBinding: %v", err)
	}

	bindings, err := ListRegisteredPullRequestBindings(fixture.projectsRoot)
	if err != nil {
		t.Fatalf("ListRegisteredPullRequestBindings: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("bindings = %#v, want exactly the one recorded binding (not the unbound claim %q)", bindings, unbound.WorktreeDir)
	}
	got := bindings[0]
	if got.Task != task || got.ClaimID != claimID || got.Repository != "acme/app" ||
		got.PullRequest != 42 || got.URL != "https://github.com/acme/app/pull/42" {
		t.Fatalf("registered binding = %#v, want task=%s claim=%s pr=42", got, task, claimID)
	}
	if got.RecordedAt.IsZero() {
		t.Fatal("registered binding RecordedAt is zero")
	}
}

// TestListRegisteredPullRequestBindingsSkipsTerminalClaims proves a claim
// whose Work Log life is already over is never returned, even though its
// pull-request sidecar is still on disk — matching
// ListActiveClaimSummaries' own terminal-skip rule the reader mirrors.
func TestListRegisteredPullRequestBindingsSkipsTerminalClaims(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "pr-binding-terminal",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]

	if _, _, err := RecordClaimPullRequestBinding(fixture.projectsRoot, result.WorktreeDir, ClaimPullRequestBinding{
		Repository: "acme/app", PullRequest: 7, URL: "https://github.com/acme/app/pull/7",
	}); err != nil {
		t.Fatalf("RecordClaimPullRequestBinding: %v", err)
	}

	if bindings, err := ListRegisteredPullRequestBindings(fixture.projectsRoot); err != nil {
		t.Fatalf("ListRegisteredPullRequestBindings before seal: %v", err)
	} else if len(bindings) != 1 {
		t.Fatalf("bindings before seal = %#v, want one", bindings)
	}

	head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
	if err := sealWorkLogForRecycle(fixture.home, result.WorktreeDir, head, "discarded"); err != nil {
		t.Fatalf("seal claim to terminal: %v", err)
	}

	bindings, err := ListRegisteredPullRequestBindings(fixture.projectsRoot)
	if err != nil {
		t.Fatalf("ListRegisteredPullRequestBindings after seal: %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("bindings after seal = %#v, want none: a terminal claim's binding must not be watched", bindings)
	}
}
