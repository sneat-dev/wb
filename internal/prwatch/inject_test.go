package prwatch

import (
	"context"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
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

// TestForgetAndRetainRestartTheTwoObservationConfirmation shows through
// behaviour what the watcher remembers: a green verdict is Terminal on its
// second identical observation, and forgetting the binding (or retaining only
// others) makes the next one a first observation again.
func TestForgetAndRetainRestartTheTwoObservationConfirmation(t *testing.T) {
	t.Parallel()
	watcher := NewWatcher()
	watcher.Observe = func(context.Context, string, string) prsnapshot.Snapshot {
		return prsnapshot.Snapshot{State: "open", Head: "h", Green: true, Checks: map[string]int{"pass": 1}}
	}
	binding := worktrees.RegisteredPullRequestBinding{Task: "t", Repository: "acme/w", PullRequest: 1}
	other := worktrees.RegisteredPullRequestBinding{Task: "t", Repository: "acme/w", PullRequest: 2}
	terminal := func() bool {
		outcome, err := watcher.Evaluate(context.Background(), binding)
		if err != nil {
			t.Fatal(err)
		}
		return outcome.Terminal
	}
	if terminal() || !terminal() {
		t.Fatal("two identical observations did not confirm")
	}
	watcher.Forget(binding)
	if terminal() || !terminal() {
		t.Fatal("a forgotten binding kept its confirmation")
	}
	watcher.Retain([]worktrees.RegisteredPullRequestBinding{binding})
	if !terminal() {
		t.Fatal("a retained binding lost its confirmation")
	}
	watcher.Retain([]worktrees.RegisteredPullRequestBinding{other})
	if terminal() {
		t.Fatal("a binding no longer listed kept its confirmation")
	}
}

// TestEvaluateRoutesItsGitHubReadsThroughTheReader shows the watcher's reads can
// be answered from memory: nothing runs `gh`.
func TestEvaluateRoutesItsGitHubReadsThroughTheReader(t *testing.T) {
	t.Parallel()
	var asked []string
	watcher := NewWatcher()
	watcher.Observe = prsnapshot.ObserveLean
	watcher.Reader = &githubobserver.Reader{Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
		asked = append(asked, request.Endpoint)
		return githubobserver.Response{Body: []byte(`{"number":3,"state":"closed","merged":true,"head":{"sha":"h"},"base":{"ref":"main"}}`), StatusCode: 200}, nil
	}}
	outcome, err := watcher.Evaluate(context.Background(), worktrees.RegisteredPullRequestBinding{Task: "t", Repository: "acme/w", PullRequest: 3})
	if err != nil || outcome.Kind != KindMerged || len(asked) != 1 {
		t.Fatalf("outcome=%+v err=%v asked=%v", outcome, err, asked)
	}
}
