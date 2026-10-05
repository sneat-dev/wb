package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver/testfixture"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestOrchCovMatchesHoldUsesPathMatchSemantics(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		slug     string
		patterns []string
		want     bool
	}{
		{name: "no patterns", slug: "acme/app"},
		{name: "blank patterns are ignored", slug: "acme/app", patterns: []string{"", "   "}},
		{name: "exact", slug: "acme/app", patterns: []string{"acme/app"}, want: true},
		{name: "owner glob", slug: "acme/app", patterns: []string{"acme/*"}, want: true},
		{name: "glob never crosses a slash", slug: "acme/team/app", patterns: []string{"acme/*"}},
		{name: "malformed pattern is ignored", slug: "acme/app", patterns: []string{"[", "acme/app"}, want: true},
		{name: "later pattern still matches", slug: "acme/app", patterns: []string{"other/app", "acme/app"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := MatchesHold(test.slug, test.patterns); got != test.want {
				t.Fatalf("MatchesHold(%q, %v) = %t, want %t", test.slug, test.patterns, got, test.want)
			}
		})
	}
}

//nolint:paralleltest // legacy external-process fixture remains serial during runner migration
func TestOrchCovRunCommandReportsATimeout(t *testing.T) {
	// runCommand's own timeout wrapping needs a real, killable child process
	// (spec/plans/coverage-to-100 task-17).
	_, attempts, err := runCommand(context.Background(), runner.New(), 20*time.Millisecond, 0, t.TempDir(), "sh", "-c", "sleep 5")
	if err == nil || !strings.Contains(err.Error(), "timed out after 20ms") {
		t.Fatalf("timed-out command error = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestOrchCovLastNonEmptyLineIgnoresTrailingBlanks(t *testing.T) {
	t.Parallel()
	if got := lastNonEmptyLine("first\n\n  second  \n\n"); got != "second" {
		t.Fatalf("lastNonEmptyLine = %q", got)
	}
	if got := lastNonEmptyLine("   \n\n"); got != "" {
		t.Fatalf("blank input = %q", got)
	}
}

func TestOrchCovFetchMemoIsANoOpWhenNilAndCountsWhenNot(t *testing.T) {
	t.Parallel()
	var nilMemo *FetchMemo
	if nilMemo.SkipFetch("/repo") {
		t.Fatal("a nil memo skipped a fetch")
	}
	if nilMemo.Skips() != 0 {
		t.Fatalf("nil memo skips = %d", nilMemo.Skips())
	}
	memo := NewFetchMemo()
	stale := time.Now().Add(-2 * FetchMemoMaxAge)
	memo.now = func() time.Time { return stale }
	memo.MarkFetched("/stale")
	memo.now = time.Now
	if memo.SkipFetch("/stale") {
		t.Fatal("a stale fetch was reused")
	}
	memo.now = time.Now
	memo.MarkFetched("/fresh")
	if !memo.SkipFetch("/fresh") {
		t.Fatal("a fresh, untouched fetch was not reused")
	}
	if memo.Skips() != 1 {
		t.Fatalf("memo skips = %d, want 1", memo.Skips())
	}
	memo.MarkTouched("/fresh")
	if memo.SkipFetch("/fresh") {
		t.Fatal("a touched clone was reused")
	}
}

func TestOrchCovGitHubReadReportsACommandFailure(t *testing.T) {
	testfixture.InstallGH(t, `#!/bin/sh
echo "boom" >&2
exit 1
`)
	if output, err := githubRead(context.Background(), "", "api", "repos/acme/app"); err == nil || output != "" {
		t.Fatalf("githubRead = %q, %v, want a failure", output, err)
	}
}

func TestOrchCovLandingLaneGuardIsANoOpWithoutAResolvableProjectsRoot(t *testing.T) {
	for _, name := range []string{"WB_HOME", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH"} {
		t.Setenv(name, "")
	}
	// The state home derives from the projects root now, so an unusable
	// projects root is passed instead of relying on an unresolvable WB_HOME.
	blocker := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectsRoot := filepath.Join(blocker, "projects")
	if err := releaseLandingLane(projectsRoot, "acme/app", "main", "session-1"); err != nil {
		t.Fatalf("release without a resolvable projects root = %v, want nil", err)
	}
	if err := refreshLandingLaneHeartbeat(projectsRoot, "acme/app", "main", "session-1"); err != nil {
		t.Fatalf("heartbeat without a resolvable projects root = %v, want nil", err)
	}
	// Acquiring, unlike releasing, must fail loudly: a guard that silently
	// did not run would let two sessions land on one target.
	if _, err := acquireLandingLane(projectsRoot, "acme/app", "main", LaneGuardRequest{
		Owner: landinglane.Owner{WBSessionID: "session-1"},
	}); err == nil {
		t.Fatal("acquire without a resolvable projects root silently skipped the guard")
	}
}

func TestOrchCovReleaseLandingLaneIgnoresAnEmptySession(t *testing.T) {
	t.Parallel()
	if err := releaseLandingLane(t.TempDir(), "acme/app", "main", "  "); err != nil {
		t.Fatalf("release with no session = %v, want nil", err)
	}
}

func TestOrchCovLandingLaneHeartbeatRefreshesTheHeldLane(t *testing.T) {
	fixture := newEngineFixture(t)
	owner := landinglane.Owner{WBSessionID: "session-1", PID: os.Getpid()}
	if _, err := acquireLandingLane(fixture.githubDir, "acme/app", "main", LaneGuardRequest{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	home, err := wbhome.Root(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	before, found, err := landinglane.Read(home, "acme/app", "main")
	if err != nil || !found {
		t.Fatalf("lane record before heartbeat: found=%t err=%v", found, err)
	}

	stop := startLandingLaneHeartbeat(fixture.githubDir, "acme/app", "main", "session-1", 5*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	var after landinglane.Record
	for time.Now().Before(deadline) {
		after, _, err = landinglane.Read(home, "acme/app", "main")
		if err != nil {
			t.Fatal(err)
		}
		if after.Owner.HeartbeatAt.After(before.Owner.HeartbeatAt) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if !after.Owner.HeartbeatAt.After(before.Owner.HeartbeatAt) {
		t.Fatalf("heartbeat did not advance: before=%s after=%s", before.Owner.HeartbeatAt, after.Owner.HeartbeatAt)
	}
	if after.Owner.WBSessionID != "session-1" {
		t.Fatalf("heartbeat changed the owner: %+v", after.Owner)
	}
}

func TestOrchCovLandingLaneHeartbeatClampsANonPositiveInterval(t *testing.T) {
	t.Parallel()
	// A non-positive interval has to be clamped to the documented default: a
	// zero duration would panic time.NewTicker inside the refresh goroutine,
	// taking the process with it.
	stop := startLandingLaneHeartbeat(t.TempDir(), "acme/app", "main", "session-1", 0)
	stop()
}

func TestOrchCovLandingLaneHeartbeatWithoutASessionIsANoOp(t *testing.T) {
	t.Parallel()
	stop := startLandingLaneHeartbeat(t.TempDir(), "acme/app", "main", "  ", time.Millisecond)
	stop()
}

func TestOrchCovWorktreeMergeLaneReleasableOnlyHoldsLiveReceipts(t *testing.T) {
	t.Parallel()
	for _, status := range []WorktreeMergeStatus{
		WorktreeMergePreparing, WorktreeMergePrepared, WorktreeMergePublished, WorktreeMergeChecksPending,
	} {
		if WorktreeMergeLaneReleasable(status) {
			t.Fatalf("a still-live receipt in status %q released its lane", status)
		}
	}
	for _, status := range []WorktreeMergeStatus{
		WorktreeMergeLanded, WorktreeMergeConflict, WorktreeMergeValidationFailed,
		WorktreeMergeChecksFailed, WorktreeMergeComplete, WorktreeMergeStatus("unknown"),
	} {
		if !WorktreeMergeLaneReleasable(status) {
			t.Fatalf("a terminal receipt in status %q kept its lane", status)
		}
	}
}

func TestOrchCovRefreshPublishedCandidateRefusesUnusableInput(t *testing.T) {
	t.Parallel()
	if err := refreshPublishedWorktreeMergeCandidateTarget(context.Background(), nil, "abc", time.Minute, 0); err == nil ||
		!strings.Contains(err.Error(), "receipt is required") {
		t.Fatalf("nil receipt error = %v", err)
	}
	if err := refreshPublishedWorktreeMergeCandidateTarget(context.Background(), &WorktreeMergeReceipt{}, "  ", time.Minute, 0); err == nil ||
		!strings.Contains(err.Error(), "remote target revision is required") {
		t.Fatalf("empty target error = %v", err)
	}
}

//nolint:paralleltest // legacy external-process fixture remains serial during runner migration
func TestOrchCovRefreshPublishedCandidateReportsAnUnmergeableTarget(t *testing.T) {
	dir := orchCovGitRepo(t)
	receipt := &WorktreeMergeReceipt{
		Candidate:   WorktreeMergeCandidate{Worktree: dir, SHA: "0123456789abcdef"},
		TargetSHA:   "fedcba9876543210",
		PullRequest: "https://example.test/acme/app/pull/41",
		ReceiptPath: "/tmp/receipt.json",
	}
	err := refreshPublishedWorktreeMergeCandidateTarget(context.Background(), receipt, "not-a-revision", time.Minute, 0)
	if err == nil || !strings.Contains(err.Error(), "failed to merge cleanly") ||
		!strings.Contains(err.Error(), "https://example.test/acme/app/pull/41") {
		t.Fatalf("unmergeable target error = %v", err)
	}
	// A refusal must leave the recorded candidate and target untouched.
	if receipt.Candidate.SHA != "0123456789abcdef" || receipt.TargetSHA != "fedcba9876543210" || len(receipt.TargetRefreshes) != 0 {
		t.Fatalf("refused refresh changed the receipt: %+v", receipt)
	}
}

//nolint:paralleltest // legacy external-process fixture remains serial during runner migration
func TestOrchCovRefreshPublishedCandidateNamesTheConflictingPaths(t *testing.T) {
	dir := orchCovGitRepo(t)
	runEngineGit(t, dir, "checkout", "-b", "side")
	writeEngineFile(t, filepath.Join(dir, "conflict.txt"), "side\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "side change")
	runEngineGit(t, dir, "checkout", "main")
	writeEngineFile(t, filepath.Join(dir, "conflict.txt"), "main\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "main change")

	receipt := &WorktreeMergeReceipt{
		Candidate:   WorktreeMergeCandidate{Worktree: dir, SHA: "0123456789abcdef"},
		TargetSHA:   "fedcba9876543210",
		PullRequest: "https://example.test/acme/app/pull/41",
		ReceiptPath: "/tmp/receipt.json",
	}
	err := refreshPublishedWorktreeMergeCandidateTarget(context.Background(), receipt, "side", time.Minute, 0)
	if err == nil || !strings.Contains(err.Error(), "conflicts in conflict.txt") ||
		!strings.Contains(err.Error(), "wb worktree merge resume /tmp/receipt.json") {
		t.Fatalf("conflicting refresh error = %v", err)
	}
	// The failed merge was aborted, so the worktree is usable again.
	if status := strings.TrimSpace(runEngineGit(t, dir, "status", "--porcelain")); status != "" {
		t.Fatalf("aborted merge left the worktree dirty: %q", status)
	}
}

//nolint:paralleltest // legacy external-process fixture remains serial during runner migration
func TestOrchCovConflictingWorktreeMergePathsReportsAGitFailure(t *testing.T) {
	if _, err := conflictingWorktreeMergePaths(context.Background(), t.TempDir()); err == nil {
		t.Fatal("conflicting paths accepted a non-repository")
	}
}

// orchCovGitRepo creates a one-commit repository on main. The repository is
// given its own identity because production code commits into it through plain
// `git` invocations (a merge, for example), which do not carry the `-c`
// overrides the fixture's own commits use. A developer machine lets git
// auto-derive an identity; a CI runner has none, and the commit fails there.
func orchCovGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runEngineGit(t, dir, "init", "-b", "main")
	runEngineGit(t, dir, "config", "user.name", "WB Test")
	runEngineGit(t, dir, "config", "user.email", "wb@example.test")
	writeEngineFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runEngineGit(t, dir, "add", "-A")
	runEngineGit(t, dir, "commit", "-m", "initial")
	return dir
}
