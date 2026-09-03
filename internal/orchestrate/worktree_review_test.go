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
	pullRequest := ReviewSubject{Repository: "sneat-co/sneat-go", PullRequest: "1041"}
	if got := ReviewTaskName(pullRequest); got != "review-sneat-co-sneat-go-1041" {
		t.Fatalf("task = %q", got)
	}
	if got := ReviewBranchName(pullRequest); got != "review/sneat-co-sneat-go-1041" {
		t.Fatalf("branch = %q", got)
	}
	// A local review is named for the ref, so two reviewers of the same branch
	// collide — the outcome to want — while different branches do not.
	local := ReviewSubject{Repository: "sneat-co/sneat-go", LocalRef: "agent/fix-login"}
	if got := ReviewTaskName(local); got != "review-sneat-co-sneat-go-agent-fix-login" {
		t.Fatalf("local task = %q", got)
	}
	if ReviewTaskName(local) == ReviewTaskName(ReviewSubject{Repository: "sneat-co/sneat-go", LocalRef: "agent/other"}) {
		t.Fatal("two branches must not share a review checkout")
	}
	if ReviewTaskName(pullRequest) == ReviewTaskName(local) {
		t.Fatal("a pull-request review and a local review of the same repository are different reviews")
	}
	if ReviewTaskName(ReviewSubject{Repository: "acme/app", PullRequest: "7"}) ==
		ReviewTaskName(ReviewSubject{Repository: "other/app", PullRequest: "7"}) {
		t.Fatal("two repositories must not share a review checkout")
	}
	// Characters a path cannot carry still yield a usable name rather than a
	// refusal.
	if got := ReviewTaskName(ReviewSubject{Repository: "Acme.Corp/My_App", PullRequest: "3"}); got != "review-acme-corp-my-app-3" {
		t.Fatalf("task = %q", got)
	}
}

// Under the local model most reviewable work never opens a pull request, so a
// review that can only be addressed by one cannot review most of the work.
func TestCreateReviewWorktreeReviewsALocalBranch(t *testing.T) {
	fixture := newLandFixture(t, "agent/fix-login", "go.mod")

	result, err := CreateReviewWorktree(context.Background(), WorktreeReviewOptions{
		Repository: "acme/app", From: "agent/fix-login", ProjectsRoot: fixture.projects,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown", Initiator: "reviewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "success" {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.PullRequest != 0 || result.LocalRef != "agent/fix-login" {
		t.Fatalf("a local review names no pull request: %#v", result)
	}
	if result.HeadSHA != fixture.headSHA {
		t.Fatalf("head = %s, want the branch tip %s", result.HeadSHA, fixture.headSHA)
	}
	if result.Task != "review-acme-app-agent-fix-login" {
		t.Fatalf("task = %q", result.Task)
	}
	manifest, err := worktrees.ReadManifest(result.WorktreeDir)
	if err != nil {
		t.Fatalf("a review checkout must carry a manifest, which is also what makes the heartbeat possible: %v", err)
	}
	if manifest.Purpose != worktrees.PurposeReview || manifest.ReviewOf != "acme/app agent/fix-login" {
		t.Fatalf("manifest = %#v", manifest)
	}
	// The heartbeat needs the journal the manifest lives beside; a checkout
	// made with `git worktree add` has neither.
	worktrees.TouchHeartbeat(result.WorktreeDir, "wb worktree info")
	if at := worktrees.HeartbeatAt(result.WorktreeDir); at.IsZero() {
		t.Fatal("a tracked review checkout must be able to record a heartbeat")
	}
}

// A ref that does not resolve is refused rather than guessed at.
func TestCreateReviewWorktreeRefusesAnUnknownRef(t *testing.T) {
	fixture := newLandFixture(t, "agent/exists", "go.mod")

	result, err := CreateReviewWorktree(context.Background(), WorktreeReviewOptions{
		Repository: "acme/app", From: "agent/never-existed", ProjectsRoot: fixture.projects,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown", Initiator: "reviewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "refused" || result.RefusalCode != ReviewRefusalUnknownRef {
		t.Fatalf("outcome = %s (%s)", result.Outcome, result.RefusalCode)
	}
	if result.SanctionedCommand == "" {
		t.Fatal("the refusal must name a way forward")
	}
}
