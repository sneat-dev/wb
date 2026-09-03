package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// A review checkout must arrive tracked: manifest, claim, owner and TTL, on a
// branch rather than a detached HEAD. The detached shape is precisely the one
// WB cannot retire, and every review used to produce one.
func TestCreateReviewWorktreeProducesATrackedCheckout(t *testing.T) {
	fixture := newLandFixture(t, "feature/reviewed", "go.mod")

	result, err := CreateReviewWorktree(context.Background(), WorktreeReviewOptions{
		Repository: "acme/app", PullRequest: "7", ProjectsRoot: fixture.projects,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown", Initiator: "reviewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "success" {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.Task != "review-acme-app-7" || result.Branch != "review/acme-app-7" {
		t.Fatalf("identity = %#v", result)
	}
	if result.HeadSHA != fixture.headSHA {
		t.Fatalf("head = %s, want the pull request's head %s", result.HeadSHA, fixture.headSHA)
	}
	if result.TTLSeconds != int64(DefaultReviewTTL.Seconds()) {
		t.Fatalf("ttl = %d", result.TTLSeconds)
	}

	// It is on a branch at the pull request's head, not detached.
	head := strings.TrimSpace(runEngineGit(t, result.WorktreeDir, "rev-parse", "HEAD"))
	if head != fixture.headSHA {
		t.Fatalf("checkout head = %s, want %s", head, fixture.headSHA)
	}
	branch := strings.TrimSpace(runEngineGit(t, result.WorktreeDir, "branch", "--show-current"))
	if branch != "review/acme-app-7" {
		t.Fatalf("checkout branch = %q, want a branch rather than a detached HEAD", branch)
	}

	// And WB knows what it is: manifest, purpose, the pull request, the TTL.
	manifest, err := worktrees.ReadManifest(result.WorktreeDir)
	if err != nil {
		t.Fatalf("a review checkout must carry a manifest: %v", err)
	}
	if manifest.Purpose != worktrees.PurposeReview || manifest.ReviewOf != "acme/app#7" || manifest.TTLSeconds == 0 {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.Base != "main" {
		t.Fatalf("manifest base = %q, want the pull request's target", manifest.Base)
	}

	// The inventory sees it as a review checkout rather than as unlanded work.
	listed, err := worktrees.ListWithDiagnostics(context.Background(), worktrees.ListOptions{
		ProjectsRoot: fixture.projects, Task: result.Task,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Results) != 1 {
		t.Fatalf("inventory = %#v", listed.Results)
	}
	if listed.Results[0].Purpose != worktrees.PurposeReview || listed.Results[0].ReviewOf != "acme/app#7" {
		t.Fatalf("inventory row = %#v", listed.Results[0])
	}
	if listed.Results[0].Detached {
		t.Fatal("a tracked review checkout is not detached")
	}
}

// A closed pull request's head is a historical fact. Reviewing it is
// deliberate, and the refusal names the flag that makes it so.
func TestCreateReviewWorktreeRefusesAClosedPullRequestWithoutASHA(t *testing.T) {
	fixture := newLandFixture(t, "feature/closed", "go.mod")
	fixture.writeState(t, "pr-state", "closed")

	result, err := CreateReviewWorktree(context.Background(), WorktreeReviewOptions{
		Repository: "acme/app", PullRequest: "7", ProjectsRoot: fixture.projects,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown", Initiator: "reviewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "refused" || result.RefusalCode != ReviewRefusalNotOpen {
		t.Fatalf("outcome = %s (%s)", result.Outcome, result.RefusalCode)
	}
	if !strings.Contains(result.SanctionedCommand, "--sha") {
		t.Fatalf("the refusal must name the flag that makes it deliberate: %q", result.SanctionedCommand)
	}

	// And with --sha it proceeds.
	explicit, err := CreateReviewWorktree(context.Background(), WorktreeReviewOptions{
		Repository: "acme/app", PullRequest: "7", ProjectsRoot: fixture.projects, SHA: fixture.headSHA,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown", Initiator: "reviewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Outcome != "success" || explicit.HeadSHA != fixture.headSHA {
		t.Fatalf("explicit review = %#v", explicit)
	}
}

// Ending a review removes the checkout even when the reviewer left scratch
// files behind, after capturing them. A review that survives because someone
// left a note in it is the same permanent debt in a new costume.
func TestReviewEndRetiresADirtyReviewCheckout(t *testing.T) {
	fixture := newLandFixture(t, "feature/dirty-review", "go.mod")
	result, err := CreateReviewWorktree(context.Background(), WorktreeReviewOptions{
		Repository: "acme/app", PullRequest: "7", ProjectsRoot: fixture.projects,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown", Initiator: "reviewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(result.WorktreeDir, "review-notes.md"), []byte("M-1 …\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	aborted, err := worktrees.Abort(context.Background(), worktrees.AbortOptions{
		ProjectsRoot: fixture.projects, Task: result.Task,
		Disposition: worktrees.AbortDiscarded, Apply: true, DeleteRemote: true,
	})
	if err != nil {
		t.Fatalf("ending a dirty review checkout must work: %v", err)
	}
	if len(aborted) != 1 || !aborted[0].Applied || aborted[0].DirtyCapture == nil {
		t.Fatalf("review end = %#v", aborted)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("review checkout survived its own end: %v", statErr)
	}
}

func TestReviewIdentityIsDerivedAndStable(t *testing.T) {
	if got := ReviewTaskName("sneat-co/sneat-go", 1041); got != "review-sneat-co-sneat-go-1041" {
		t.Fatalf("task = %q", got)
	}
	if got := ReviewBranchName("sneat-co/sneat-go", 1041); got != "review/sneat-co-sneat-go-1041" {
		t.Fatalf("branch = %q", got)
	}
	// A second reviewer of the same pull request collides with the first, which
	// is a far better outcome than two checkouts nobody can tell apart. Two
	// different pull requests must not.
	if ReviewTaskName("acme/app", 7) == ReviewTaskName("acme/app", 8) {
		t.Fatal("two pull requests must not share a review checkout")
	}
	if ReviewTaskName("acme/app", 7) == ReviewTaskName("other/app", 7) {
		t.Fatal("two repositories must not share a review checkout")
	}
	// An owner or repository with characters a path cannot carry still yields a
	// usable task name rather than a refusal.
	if got := ReviewTaskName("Acme.Corp/My_App", 3); got != "review-acme-corp-my-app-3" {
		t.Fatalf("task = %q", got)
	}
}
