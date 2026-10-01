package prwatch

import (
	"context"
	"testing"

	"github.com/sneat-dev/wb/internal/prsnapshot"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestEvaluateUsesTheInjectedObserverAndKeepsItsSnapshot(t *testing.T) {
	t.Parallel()
	watcher := NewWatcher()
	var asked []string
	watcher.Observe = func(_ context.Context, repository, selector string) prsnapshot.Snapshot {
		asked = append(asked, repository+"#"+selector)
		return prsnapshot.Snapshot{State: "open", Draft: true, Head: "abc", Mergeable: "blocked", Checks: map[string]int{"pass": 2}, Green: true}
	}
	binding := worktrees.RegisteredPullRequestBinding{Task: "t", Repository: "acme/w", PullRequest: 4}
	outcome, err := watcher.Evaluate(context.Background(), binding)
	if err != nil || len(asked) != 1 || asked[0] != "acme/w#4" {
		t.Fatalf("outcome=%+v err=%v asked=%v", outcome, err, asked)
	}
	if !outcome.Snapshot.Draft || outcome.Snapshot.Mergeable != "blocked" || !outcome.Snapshot.Green || outcome.Snapshot.Checks["pass"] != 2 {
		t.Fatalf("snapshot not carried: %+v", outcome.Snapshot)
	}
}

func TestForgetAndRetainDropRememberedBindings(t *testing.T) {
	t.Parallel()
	watcher := NewWatcher()
	first := worktrees.RegisteredPullRequestBinding{Task: "t", Repository: "acme/w", PullRequest: 1}
	second := worktrees.RegisteredPullRequestBinding{Task: "t", Repository: "acme/w", PullRequest: 2}
	third := worktrees.RegisteredPullRequestBinding{Task: "t", Repository: "acme/w", PullRequest: 3}
	for _, binding := range []worktrees.RegisteredPullRequestBinding{first, second, third} {
		watcher.remember(binding, KindChecksPassed, "h")
	}
	watcher.Forget(first)
	watcher.Retain([]worktrees.RegisteredPullRequestBinding{second})
	if len(watcher.seen) != 1 {
		t.Fatalf("remembered %v, want only the second binding", watcher.seen)
	}
	if _, ok := watcher.seen[bindingKey(second)]; !ok {
		t.Fatalf("the retained binding was dropped: %v", watcher.seen)
	}
}
