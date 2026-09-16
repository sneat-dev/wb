package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWTCoreCovGCEntryRendersEveryRowShape asserts the inventory row names a
// detached checkout by its head, names the disposition that decides it, and
// renders ages on the documented boundaries.
func TestWTCoreCovGCEntryRendersEveryRowShape(t *testing.T) {
	head := strings.Repeat("a", 40)
	detached := GCEntry{Task: "task", Repository: "acme/app", HeadSHA: head, Class: "detached_review", Owner: "none", AgeSeconds: 120}
	row := detached.String()
	if !strings.Contains(row, "DETACHED "+shortSHA(head)) {
		t.Fatalf("detached row = %q", row)
	}
	if !strings.Contains(row, "keep") {
		t.Fatalf("unapplied row = %q", row)
	}
	eligible := detached
	eligible.Eligible = true
	if !strings.Contains(eligible.String(), "would retire") {
		t.Fatalf("eligible row = %q", eligible.String())
	}
	applied := eligible
	applied.Applied = true
	if !strings.Contains(applied.String(), "retired") {
		t.Fatalf("applied row = %q", applied.String())
	}
	if got := humanAge(0); got != "-" {
		t.Fatalf("humanAge(0) = %q", got)
	}
	if got := humanAge(3599); got != "59m" {
		t.Fatalf("humanAge(3599) = %q", got)
	}
	if got := humanAge(3600); got != "1h" {
		t.Fatalf("humanAge(3600) = %q", got)
	}
	if got := humanAge(86399); got != "23h" {
		t.Fatalf("humanAge(86399) = %q", got)
	}
	if got := humanAge(86400); got != "1d" {
		t.Fatalf("humanAge(86400) = %q", got)
	}
	if got := residueDepthOrDefault(0); got != DefaultResidueDepth {
		t.Fatalf("residueDepthOrDefault(0) = %d", got)
	}
	if got := residueDepthOrDefault(3); got != 3 {
		t.Fatalf("residueDepthOrDefault(3) = %d", got)
	}
}

// TestWTCoreCovDetachedReviewReasonNamesTheEvidence asserts the classifier's
// reason names the fact that decided it, in the documented precedence.
func TestWTCoreCovDetachedReviewReasonNamesTheEvidence(t *testing.T) {
	merged := detachedReviewReason(ListResult{MergedPullRequest: &PullRequest{URL: "https://example.test/pr/1"}})
	if !strings.Contains(merged, "merged pull request https://example.test/pr/1") {
		t.Fatalf("merged reason = %q", merged)
	}
	absorbed := detachedReviewReason(ListResult{AbsorbedAtOrigin: true, AbsorbedBySHA: strings.Repeat("b", 40)})
	if !strings.Contains(absorbed, "landed at "+shortSHA(strings.Repeat("b", 40))) {
		t.Fatalf("absorbed reason = %q", absorbed)
	}
	contained := detachedReviewReason(ListResult{})
	if !strings.Contains(contained, "contained in the fetched origin target") {
		t.Fatalf("contained reason = %q", contained)
	}
	err := invalidManagedWorktreePath("/checkout", "/trees")
	if !strings.Contains(err.Error(), "/trees/<task>/<owner>/<repository>") {
		t.Fatalf("invalid managed path error = %q", err)
	}
}

// TestWTCoreCovWorktreeAddArguments asserts the two Git argument shapes: an
// existing branch is checked out, a new one is created from the base revision.
func TestWTCoreCovWorktreeAddArguments(t *testing.T) {
	existing := worktreeAddArguments("/checkout", "wb/task", "base", true)
	if got := strings.Join(existing, " "); got != "worktree add --quiet /checkout wb/task" {
		t.Fatalf("existing branch arguments = %q", got)
	}
	created := worktreeAddArguments("/checkout", "wb/task", "base", false)
	if got := strings.Join(created, " "); got != "worktree add --quiet -b wb/task /checkout base" {
		t.Fatalf("new branch arguments = %q", got)
	}
}

// TestWTCoreCovHeldOperationLockLifecycle asserts the public lock wrapper
// reports its state honestly across acquisition, reclaim, release, and
// preservation, and that a nil wrapper is inert.
func TestWTCoreCovHeldOperationLockLifecycle(t *testing.T) {
	var absent *HeldOperationLock
	if absent.File() != nil || absent.ReclaimedInterrupted() {
		t.Fatal("a nil held lock reported state")
	}
	if err := absent.Release(); err != nil {
		t.Fatalf("releasing a nil held lock = %v", err)
	}
	absent.Preserve()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	lock, err := AcquireOperationLock(directory, false)
	if err != nil {
		t.Fatalf("AcquireOperationLock: %v", err)
	}
	if lock.File() == nil || lock.ReclaimedInterrupted() {
		t.Fatalf("fresh lock = file=%v interrupted=%v", lock.File(), lock.ReclaimedInterrupted())
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// An unheld remnant is reclaimed and reported as interrupted, so a caller
	// can validate ownership before resuming or preserving it.
	remnant := filepath.Join(root, ".lock")
	if err := os.WriteFile(remnant, []byte("interrupted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := AcquireOperationLock(directory, true)
	if err != nil {
		t.Fatalf("reclaiming an interrupted lock: %v", err)
	}
	if !reclaimed.ReclaimedInterrupted() {
		t.Fatal("a reclaimed remnant was not reported as interrupted")
	}
	reclaimed.Preserve()
	if reclaimed.File() != nil {
		t.Fatal("Preserve left the descriptor attached")
	}
	if err := reclaimed.Release(); err != nil {
		t.Fatalf("releasing a preserved lock = %v", err)
	}
	if _, err := os.Stat(remnant); err != nil {
		t.Fatalf("Preserve removed the ambiguous remnant: %v", err)
	}
}

// TestWTCoreCovLogVerbHelpersRefuseIncompleteRequests asserts the handoff verb
// requires its two mandatory declarations, and the publish probe distinguishes
// no branch, an unpublished branch, and a published one.
func TestWTCoreCovLogVerbHelpersRefuseIncompleteRequests(t *testing.T) {
	ctx := context.Background()
	repository := newJournalWorktree(t)
	if err := os.WriteFile(filepath.Join(repository, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "seed.txt")
	gitTest(t, repository, "commit", "-m", "base")

	published, err := branchPublished(ctx, repository)
	if err != nil {
		t.Fatalf("branchPublished: %v", err)
	}
	if published {
		t.Fatal("a branch with no remote tracking ref was reported as published")
	}
	gitTest(t, repository, "update-ref", "refs/remotes/origin/"+wtCoreCovCurrentBranch(t, repository), strings.TrimSpace(gitTestOutput(t, repository, "rev-parse", "HEAD")))
	published, err = branchPublished(ctx, repository)
	if err != nil || !published {
		t.Fatalf("published branch = %v, err=%v", published, err)
	}
	gitTest(t, repository, "checkout", "--detach")
	published, err = branchPublished(ctx, repository)
	if err != nil || published {
		t.Fatalf("detached branchPublished = %v, err=%v", published, err)
	}
	if _, err := branchPublished(ctx, filepath.Join(t.TempDir(), "not-a-repository")); err == nil {
		t.Fatal("branchPublished outside a repository must fail")
	}

	if _, err := LogHandoff(ctx, LogHandoffOptions{}); err == nil {
		t.Fatal("a handoff without a worktree was accepted")
	}
	if _, err := LogHandoff(ctx, LogHandoffOptions{Worktree: repository}); err == nil || !strings.Contains(err.Error(), "--summary is required") {
		t.Fatalf("handoff without a summary = %v", err)
	}
	if _, err := LogHandoff(ctx, LogHandoffOptions{Worktree: repository, Summary: "reason"}); err == nil || !strings.Contains(err.Error(), "--successor is required") {
		t.Fatalf("handoff without a successor = %v", err)
	}
}

// TestWTCoreCovWorkLogOptionHelpers asserts the stdin prompt capture is
// byte-exact and fails closed on empty input, and that execution identifiers
// follow one shared rule.
func TestWTCoreCovWorkLogOptionHelpers(t *testing.T) {
	if _, err := (WorkLogOptions{}).WithOriginalPromptFromStdin([]byte("   \n\t")); err == nil {
		t.Fatal("whitespace-only stdin was accepted as an original prompt")
	}
	content := []byte("do the thing\n")
	options, err := (WorkLogOptions{}).WithOriginalPromptFromStdin(content)
	if err != nil {
		t.Fatalf("WithOriginalPromptFromStdin: %v", err)
	}
	if options.OriginalPrompt != originalPromptStdinMarker {
		t.Fatalf("marker = %q", options.OriginalPrompt)
	}
	if string(options.originalPromptContents) != string(content) || options.originalPromptDigest == "" {
		t.Fatalf("captured prompt = %#v", options)
	}
	content[0] = 'X'
	if options.originalPromptContents[0] != 'd' {
		t.Fatal("captured prompt aliases the caller's buffer")
	}

	for _, testCase := range []struct {
		value        string
		allowUnknown bool
		want         bool
	}{
		{value: "unknown", allowUnknown: false, want: false},
		{value: "unknown", allowUnknown: true, want: true},
		{value: "claude-sonnet-4.5", want: true},
		{value: "codex", want: true},
		{value: "", want: false},
		{value: "with space", want: false},
		{value: "-leading-dash", want: false},
	} {
		if got := ValidExecutionIdentifier(testCase.value, testCase.allowUnknown); got != testCase.want {
			t.Fatalf("ValidExecutionIdentifier(%q, %v) = %v, want %v", testCase.value, testCase.allowUnknown, got, testCase.want)
		}
	}
}

// TestWTCoreCovAdoptRefusesAmbiguousSelection asserts the sweep requires
// exactly one selection mode and refuses a path that is not a linked worktree
// of a canonical clone under the projects root.
func TestWTCoreCovAdoptRefusesAmbiguousSelection(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	if _, err := Adopt(ctx, AdoptOptions{ProjectsRoot: fixture.projectsRoot}); err == nil {
		t.Fatal("adopt with no selection was accepted")
	} else if !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("error %q does not name the selection rule", err)
	}
	if _, err := Adopt(ctx, AdoptOptions{ProjectsRoot: fixture.projectsRoot, Path: fixture.canonical, AllExternal: true}); err == nil {
		t.Fatal("adopt with both selections was accepted")
	}
	notAWorktree := t.TempDir()
	if _, err := Adopt(ctx, AdoptOptions{ProjectsRoot: fixture.projectsRoot, Path: notAWorktree}); err == nil {
		t.Fatal("adopt of an unrelated directory was accepted")
	} else if !strings.Contains(err.Error(), "is not a linked Git worktree") {
		t.Fatalf("error %q does not name the linkage rule", err)
	}
}

// TestWTCoreCovCreateAdoptionRegistrationIsIdempotentAndExclusive asserts the
// registration entry records the exact worktree once, refuses to be repointed
// at a different one, and refuses a path that already holds a Git worktree.
func TestWTCoreCovCreateAdoptionRegistrationIsIdempotentAndExclusive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	worktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "init")
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, other, "init")

	operation, err := prepareOperationRoot(home, "adopt-task", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.close()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	path, err := createAdoptionRegistration(operation.Directory, operation.Path, "acme", "app", worktree, now)
	if err != nil {
		t.Fatalf("createAdoptionRegistration: %v", err)
	}
	again, err := createAdoptionRegistration(operation.Directory, operation.Path, "acme", "app", worktree, now)
	if err != nil || again != path {
		t.Fatalf("idempotent registration = %q, err=%v, want %q", again, err, path)
	}
	if _, err := createAdoptionRegistration(operation.Directory, operation.Path, "acme", "app", other, now); err == nil {
		t.Fatal("a registration was repointed at a different worktree")
	} else if !strings.Contains(err.Error(), "already adopted for a different worktree") {
		t.Fatalf("error %q does not name the conflict", err)
	}

	// A registration directory that already holds a real Git worktree is a
	// collision, not something to overwrite.
	occupied := filepath.Join(operation.Path, "acme", "occupied")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, occupied, "init")
	if _, err := createAdoptionRegistration(operation.Directory, operation.Path, "acme", "occupied", worktree, now); err == nil {
		t.Fatal("a registration path holding a Git worktree was accepted")
	} else if !strings.Contains(err.Error(), "already holds a Git worktree") {
		t.Fatalf("error %q does not name the collision", err)
	}
}

// TestWTCoreCovReadAdoptedWorktreePointerRejectsMalformedRecords asserts the
// reconnaissance reader refuses every record it cannot trust.
func TestWTCoreCovReadAdoptedWorktreePointerRejectsMalformedRecords(t *testing.T) {
	directory := t.TempDir()
	if _, ok := readAdoptedWorktreePointer(directory); ok {
		t.Fatal("a directory with no pointer was accepted")
	}
	pointer := filepath.Join(directory, adoptedWorktreePointerName)
	for _, malformed := range []string{
		"{not json",
		`{"version":2,"worktree":"/tmp/x"}`,
		`{"version":1,"worktree":"relative/path"}`,
		`{"version":1,"worktree":""}`,
	} {
		if err := os.WriteFile(pointer, []byte(malformed), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := readAdoptedWorktreePointer(directory); ok {
			t.Fatalf("malformed pointer %q was accepted", malformed)
		}
	}
	if err := os.WriteFile(pointer, []byte(`{"version":1,"worktree":"/tmp/checkout/"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := readAdoptedWorktreePointer(directory)
	if !ok || got != "/tmp/checkout" {
		t.Fatalf("valid pointer = %q, ok=%v", got, ok)
	}
}

// TestWTCoreCovOrphanedClaimInputValidation asserts every refusal the
// orphaned-abort entry point makes before it touches any state.
func TestWTCoreCovOrphanedClaimInputValidation(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range []struct {
		name    string
		options AbortOptions
		wantErr string
	}{
		{name: "missing claim", options: AbortOptions{}, wantErr: "one exact --claim ID"},
		{name: "bad claim id", options: AbortOptions{ClaimID: "not/a/claim"}, wantErr: "one exact --claim ID"},
		{name: "missing actor", options: AbortOptions{ClaimID: strings.Repeat("a", 64)}, wantErr: "bounded single-line --actor"},
		{name: "multi-line actor", options: AbortOptions{ClaimID: strings.Repeat("a", 64), Actor: "a\nb"}, wantErr: "bounded single-line --actor"},
		{name: "oversize actor", options: AbortOptions{ClaimID: strings.Repeat("a", 64), Actor: strings.Repeat("x", 201)}, wantErr: "bounded single-line --actor"},
		{name: "missing reason", options: AbortOptions{ClaimID: strings.Repeat("a", 64), Actor: "tester"}, wantErr: "bounded single-line --reason"},
		{name: "oversize reason", options: AbortOptions{ClaimID: strings.Repeat("a", 64), Actor: "tester", Reason: strings.Repeat("x", 1001)}, wantErr: "bounded single-line --reason"},
		{name: "remote deletion", options: AbortOptions{ClaimID: strings.Repeat("a", 64), Actor: "tester", Reason: "gone", DeleteRemote: true}, wantErr: "never accepts --remote"},
		{name: "all", options: AbortOptions{ClaimID: strings.Repeat("a", 64), Actor: "tester", Reason: "gone", All: true}, wantErr: "does not accept --all"},
		{name: "missing task", options: AbortOptions{ClaimID: strings.Repeat("a", 64), Actor: "tester", Reason: "gone", ProjectsRoot: t.TempDir()}, wantErr: "task is required"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := abortOrphanedClaim(ctx, testCase.options); err == nil {
				t.Fatal("invalid input was accepted")
			} else if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error %q does not mention %q", err, testCase.wantErr)
			}
		})
	}
}

// TestWTCoreCovValidateOrphanedClaimIdentityRejectsIncompleteClaims asserts the
// identity gate refuses every incomplete or self-inconsistent claim.
func TestWTCoreCovValidateOrphanedClaimIdentityRejectsIncompleteClaims(t *testing.T) {
	base := workLogClaim{
		Version: 1, Lifecycle: "active", EffortID: "task", RunID: "run",
		ClaimID: strings.Repeat("a", 64), Task: "task", Repository: "acme/app",
		Worktree: "/checkout", Branch: "wb/task", Base: "main", BaseSHA: strings.Repeat("b", 40),
	}
	if err := validateOrphanedClaimIdentity(base); err == nil {
		t.Fatal("a claim whose digest does not match its identity was accepted")
	}
	// The digest-mismatch answer is the expected one for an otherwise valid
	// shape, which proves the earlier gates passed.
	if err := validateOrphanedClaimIdentity(base); !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("error = %v, want a digest mismatch", err)
	}

	mutate := func(change func(claim *workLogClaim)) workLogClaim {
		claim := base
		change(&claim)
		return claim
	}
	cases := map[string]workLogClaim{
		"unsupported version":  mutate(func(c *workLogClaim) { c.Version = 3 }),
		"not active":           mutate(func(c *workLogClaim) { c.Lifecycle = "terminal" }),
		"unsafe effort id":     mutate(func(c *workLogClaim) { c.EffortID = "bad/effort" }),
		"unsafe run id":        mutate(func(c *workLogClaim) { c.RunID = "bad run" }),
		"short claim id":       mutate(func(c *workLogClaim) { c.ClaimID = "short" }),
		"empty task":           mutate(func(c *workLogClaim) { c.Task = "" }),
		"empty repository":     mutate(func(c *workLogClaim) { c.Repository = "" }),
		"relative worktree":    mutate(func(c *workLogClaim) { c.Worktree = "relative" }),
		"invalid branch":       mutate(func(c *workLogClaim) { c.Branch = "bad..branch" }),
		"non-object base sha":  mutate(func(c *workLogClaim) { c.BaseSHA = "not-a-sha" }),
		"successor no agent":   mutate(func(c *workLogClaim) { c.ParentClaimID = strings.Repeat("c", 64); c.AcquiredVia = "handoff" }),
		"successor bad parent": mutate(func(c *workLogClaim) { c.ParentClaimID = "short"; c.AgentID = "agent"; c.AcquiredVia = "handoff" }),
		"unknown acquisition": mutate(func(c *workLogClaim) {
			c.ParentClaimID = strings.Repeat("c", 32)
			c.AgentID = "agent"
			c.AcquiredVia = "made-up"
		}),
	}
	for name, claim := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateOrphanedClaimIdentity(claim); err == nil {
				t.Fatal("an invalid claim identity was accepted")
			}
		})
	}
}

// wtCoreCovCurrentBranch reports the checked-out branch name of repository.
func wtCoreCovCurrentBranch(t *testing.T, repository string) string {
	t.Helper()
	return strings.TrimSpace(gitTestOutput(t, repository, "branch", "--show-current"))
}
