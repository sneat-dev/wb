package orchestrate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/gitcli/gitclitest"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// This file closes review round 2's B1/B2/B4 findings on task-17's
// coverage-to-100 migration (spec/plans/coverage-to-100): the
// resolveGit()/resolveRunner() seam PullRequestLandOptions and
// WorktreeMergeLandOptions carry (ports.go) was structurally correct but
// never actually substituted by any test, so its non-nil branch -- and two
// of the error-propagation paths it exists to make provable -- sat at
// coverage count 0. Each test below injects a gitclitest.Fake or
// runnertest.Fake through the option field the seam was built for, proving
// it is not just shaped right but actually reachable without starting a
// real git or gh process.

// TestPullRequestLandOptionsResolveGitReturnsTheInjectedGit covers
// pr_land.go's resolveGit() non-nil branch: a caller that sets options.git
// gets that exact value back, never defaultGit.
func TestPullRequestLandOptionsResolveGitReturnsTheInjectedGit(t *testing.T) {
	t.Parallel()
	fake := &gitclitest.Fake{}
	options := PullRequestLandOptions{git: fake}
	if got := options.resolveGit(); got != Git(fake) {
		t.Fatalf("resolveGit() with an injected git = %v, want the fake itself", got)
	}
}

// TestPullRequestLandOptionsResolveRunnerReturnsTheInjectedRunner covers
// pr_land.go's resolveRunner() non-nil branch, the same shape as
// resolveGit() above for this package's non-git-port runner.Runner seam.
func TestPullRequestLandOptionsResolveRunnerReturnsTheInjectedRunner(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	options := PullRequestLandOptions{run: fake}
	if got := options.resolveRunner(); got != runner.Runner(fake) {
		t.Fatalf("resolveRunner() with an injected run = %v, want the fake itself", got)
	}
}

// TestWorktreeMergeLandOptionsResolveGitReturnsTheInjectedGit covers
// worktree_merge.go's own resolveGit() non-nil branch: WorktreeMergeLandOptions
// carries the identical seam PullRequestLandOptions does, for `wb worktree
// merge`'s land/resume path.
func TestWorktreeMergeLandOptionsResolveGitReturnsTheInjectedGit(t *testing.T) {
	t.Parallel()
	fake := &gitclitest.Fake{}
	options := WorktreeMergeLandOptions{git: fake}
	if got := options.resolveGit(); got != Git(fake) {
		t.Fatalf("resolveGit() with an injected git = %v, want the fake itself", got)
	}
}

// TestCommitExistsLocallyTreatsAnEmptySHAAsAbsentWithoutCallingGit covers
// commitExistsLocally's empty-sha guard (worktree_merge_pr_land.go). The
// Fake is deliberately left with every map nil, so any call the guard did
// not actually short-circuit before would panic (gitclitest.Fake's
// documented behaviour for an unscripted case) instead of quietly
// returning a zero value -- reaching the assertion below is itself proof
// the guard ran before touching git at all.
func TestCommitExistsLocallyTreatsAnEmptySHAAsAbsentWithoutCallingGit(t *testing.T) {
	t.Parallel()
	fake := &gitclitest.Fake{}
	exists, err := commitExistsLocally(context.Background(), fake, "/some/worktree", "")
	if exists || err != nil {
		t.Fatalf(`commitExistsLocally(sha="") = (%t, %v), want (false, nil)`, exists, err)
	}
}

// TestVerifyUpdateBranchMergeProofPropagatesACommitExistsLocallyError
// covers verifyUpdateBranchMergeProof's B5 fix (worktree_merge_pr_land.go):
// a real command failure from commitExistsLocally -- including a guarded
// runner refusing to start the process at all -- must come back to the
// caller as an error, never be folded into headLocal=false the way an
// ordinary "object absent" negative is.
func TestVerifyUpdateBranchMergeProofPropagatesACommitExistsLocallyError(t *testing.T) {
	t.Parallel()
	const worktree = "/fake/worktree"
	const headSHA = "abc123headsha"
	wantErr := errors.New("boom: guarded runner refused to start")
	fake := &gitclitest.Fake{
		CommitObjectExistsByCase: map[string]gitclitest.BoolResult{
			worktree + "\x00" + headSHA: {Err: wantErr},
		},
	}

	proved, err := verifyUpdateBranchMergeProof(context.Background(), fake, worktree, "", "main", "acme/app", "candidatesha", "targetparentsha", headSHA)
	if proved {
		t.Fatalf("proved = true, want false when commitExistsLocally errors")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to be (or wrap) %v", err, wantErr)
	}
}

// TestVerifyUpdateBranchMergeProofPropagatesACommitExistsLocallyErrorAfterFetch
// (worktree_merge_pr_land.go:464-465, the second of the function's two
// commitExistsLocally error returns) lives in pr_land_test.go, next to the
// landFixture it reuses for a real worktree/origin pair rather than building
// one here.

// TestBuildAtSurfacesAFailingBuildThroughTheInjectedRunner covers
// pr_land_keep.go's buildAt: a kept commit whose build fails must be
// refused with the failing command's own captured output, reached here
// through a runnertest.Fake rather than a real `go build` in the unit
// tier.
func TestBuildAtSurfacesAFailingBuildThroughTheInjectedRunner(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	writeEngineFile(t, filepath.Join(worktree, "go.mod"), "module example.test/keepbuild\n\ngo 1.24\n")

	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"go", "build", "./..."},
		runner.Result{Stdout: "compiling\n", Stderr: "undefined: Foo\n"},
		errors.New("exit status 2"))

	refusal := buildAt(context.Background(), fake, worktree, SourceCommit{SHA: "0123456789abcdef", Subject: "add thing"}, nil)
	if refusal == nil || refusal.code != LandRefusalKeepDoesNotBuild {
		t.Fatalf("failing build refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.reason, "does not build") || !strings.Contains(refusal.reason, "undefined: Foo") {
		t.Fatalf("failing build refusal reason = %q, want it to surface the injected runner's captured output", refusal.reason)
	}
}

// TestSyncLocalWorktreeAfterUpdateBranchUsesTheInjectedGitForTheFastForward
// covers pr_land_local_sync.go's #611 call site: syncLocalWorktreeAfterUpdateBranch
// now resolves its Git port through options.resolveGit() rather than a
// package-level var, and this proves a landing that reaches it can be
// driven entirely through the injected Fake once the real worktree lookup
// (a plain `git worktree list --porcelain`, unaffected by the Fake) finds
// the branch's local checkout.
//
//nolint:paralleltest // calls a fixture helper (newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestSyncLocalWorktreeAfterUpdateBranchUsesTheInjectedGitForTheFastForward(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "task-sync-seam", "feature/sync-seam", "seam.txt", "seam\n")

	statusErr := errors.New("boom: injected status failure")
	fake := &gitclitest.Fake{
		StatusPorcelainByDir: map[string]gitclitest.Result{
			source.WorktreeDir: {Err: statusErr},
		},
	}
	options := PullRequestLandOptions{
		ProjectsRoot: fixture.githubDir,
		Repository:   fixture.repository.Slug,
		git:          fake,
	}

	note := syncLocalWorktreeAfterUpdateBranch(context.Background(), options, "feature/sync-seam", "deadbeef")
	if !strings.Contains(note, statusErr.Error()) {
		t.Fatalf("note = %q, want it to surface the injected git's StatusPorcelain error (proving options.resolveGit() reached the fake)", note)
	}
}

// TestFastForwardWorktreeToUpdatedHeadNotesUncommittedChanges covers
// fastForwardWorktreeToUpdatedHead's dirty-worktree branch
// (pr_land_local_sync.go): this exact statement was covered on main by
// TestLandLeavesADirtyWorktreeUntouched before that test moved to the e2e
// tier (fastForwardWorktreeToUpdatedHead now resolves its Git port through
// orchestrateGit), so it regressed to uncovered in the unit tier even
// though the statement itself is unchanged. Reached the same way as the
// error-propagation test above, through an injected Fake rather than a
// real dirty worktree.
//
//nolint:paralleltest // calls a fixture helper (newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestFastForwardWorktreeToUpdatedHeadNotesUncommittedChanges(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "task-dirty-seam", "feature/dirty-seam", "seam3.txt", "seam3\n")

	fake := &gitclitest.Fake{
		StatusPorcelainByDir: map[string]gitclitest.Result{
			source.WorktreeDir: {Value: " M dirty.txt\n"},
		},
	}
	options := PullRequestLandOptions{
		ProjectsRoot: fixture.githubDir,
		Repository:   fixture.repository.Slug,
		git:          fake,
	}

	note := syncLocalWorktreeAfterUpdateBranch(context.Background(), options, "feature/dirty-seam", "deadbeef")
	if !strings.Contains(note, "uncommitted changes") {
		t.Fatalf("note = %q, want it to report uncommitted changes", note)
	}
}

// TestSyncLocalWorktreeAfterUpdateBranchReturnsEmptyWhenTheCanonicalPathFails
// covers syncLocalWorktreeAfterUpdateBranch's own early guard
// (pr_land_local_sync.go): an unresolvable repository address must not be
// reported as a landing obstacle -- neither this call's to make, the way
// the function's own doc comment describes every failure downstream of it.
func TestSyncLocalWorktreeAfterUpdateBranchReturnsEmptyWhenTheCanonicalPathFails(t *testing.T) {
	t.Parallel()
	note := syncLocalWorktreeAfterUpdateBranch(context.Background(), PullRequestLandOptions{
		ProjectsRoot: t.TempDir(), Repository: "not-a-valid-repository-address",
	}, "some-branch", "deadbeef")
	if note != "" {
		t.Fatalf("note = %q, want empty when the repository address cannot resolve to a canonical path", note)
	}
}

// TestFastForwardWorktreeToUpdatedHeadReturnsEmptyForABlankWorktree covers
// fastForwardWorktreeToUpdatedHead's own blank-worktree guard
// (pr_land_local_sync.go), the same regressed-by-an-e2e-tier-move shape as
// the uncommitted-changes test above.
func TestFastForwardWorktreeToUpdatedHeadReturnsEmptyForABlankWorktree(t *testing.T) {
	t.Parallel()
	note := fastForwardWorktreeToUpdatedHead(context.Background(), &gitclitest.Fake{}, "   ", "branch", "head")
	if note != "" {
		t.Fatalf("note = %q, want empty for a blank worktree", note)
	}
}

// TestFastForwardWorktreeToUpdatedHeadPropagatesABranchShowCurrentError
// covers the same function's BranchShowCurrent error-propagation branch,
// reached through an injected gitclitest.Fake exactly like
// TestFastForwardWorktreeToUpdatedHeadNotesUncommittedChanges above, but
// directly rather than through syncLocalWorktreeAfterUpdateBranch's real
// worktree lookup -- no real worktree registration is needed to prove this
// one Git-port error return.
func TestFastForwardWorktreeToUpdatedHeadPropagatesABranchShowCurrentError(t *testing.T) {
	t.Parallel()
	const worktree = "/fake/worktree"
	wantErr := errors.New("boom: branch show-current failed")
	fake := &gitclitest.Fake{
		StatusPorcelainByDir:   map[string]gitclitest.Result{worktree: {Value: ""}},
		BranchShowCurrentByDir: map[string]gitclitest.Result{worktree: {Err: wantErr}},
	}
	note := fastForwardWorktreeToUpdatedHead(context.Background(), fake, worktree, "branch", "head")
	if !strings.Contains(note, wantErr.Error()) {
		t.Fatalf("note = %q, want it to contain %q", note, wantErr.Error())
	}
}

// TestFastForwardWorktreeToUpdatedHeadNotesHeadIsNotOnTheBranch covers the
// same function's branch-mismatch note, the same shape as the two tests
// above.
func TestFastForwardWorktreeToUpdatedHeadNotesHeadIsNotOnTheBranch(t *testing.T) {
	t.Parallel()
	const worktree = "/fake/worktree"
	fake := &gitclitest.Fake{
		StatusPorcelainByDir:   map[string]gitclitest.Result{worktree: {Value: ""}},
		BranchShowCurrentByDir: map[string]gitclitest.Result{worktree: {Value: "other-branch"}},
	}
	note := fastForwardWorktreeToUpdatedHead(context.Background(), fake, worktree, "expected-branch", "head")
	if !strings.Contains(note, "HEAD is not on expected-branch") {
		t.Fatalf("note = %q, want it to name the branch mismatch", note)
	}
}

// TestResolveReviewProofCheckoutFindsARegisteredWorktreeForTheHeadBranch
// covers resolveReviewProofCheckout's registered-worktree branch
// (pr_review_stale.go): also regressed to uncovered in the unit tier by an
// earlier round's e2e-tier test move, on unchanged code. No Fake is needed
// here -- this seam only reaches locateBranchCheckout, which is a plain
// `git worktree list --porcelain` lookup unaffected by the git/run option
// fields.
//
//nolint:paralleltest // calls a fixture helper (newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestResolveReviewProofCheckoutFindsARegisteredWorktreeForTheHeadBranch(t *testing.T) {
	fixture := newEngineFixture(t)
	createMergeSource(t, fixture, "task-review-checkout", "feature/review-checkout", "seam4.txt", "seam4\n")

	options := PullRequestLandOptions{ProjectsRoot: fixture.githubDir, Repository: fixture.repository.Slug}
	var view PullRequestView
	view.Head.Ref = "feature/review-checkout"
	view.Base.Ref = "main"

	worktree, branch, found := resolveReviewProofCheckout(context.Background(), options, view)
	if !found {
		t.Fatalf("resolveReviewProofCheckout found = false, want true for a registered worktree on %q", view.Head.Ref)
	}
	if branch != view.Head.Ref {
		t.Fatalf("branch = %q, want %q", branch, view.Head.Ref)
	}
	if filepath.Clean(worktree) != filepath.Clean(fixture.canonical) {
		t.Fatalf("worktree = %q, want the fixture's canonical dir %q", worktree, fixture.canonical)
	}
}

// keptCommitsStubGit answers only the Git methods
// rewriteBranchForKeptCommits (pr_land_keep.go) actually calls. It exists
// because that function generates its own scratch worktree path with
// os.MkdirTemp, which a fresh gitclitest.Fake cannot pre-script (its maps
// are keyed by the exact directory string, unknowable before the call).
// Embedding Git leaves every other method nil, which is safe here because
// rewriteBranchForKeptCommits never reaches them.
type keptCommitsStubGit struct {
	Git

	worktreeAddErr        error
	cherryPickNoCommitErr error
	cherryPickErr         error
	commitNoVerifyErr     error
	revParseValue         string
	revParseErr           error
	pushErr               error
}

func (g *keptCommitsStubGit) WorktreeAddDetached(context.Context, string, string, string) error {
	return g.worktreeAddErr
}

func (g *keptCommitsStubGit) WorktreeRemoveForce(context.Context, string, string) error {
	return nil
}

func (g *keptCommitsStubGit) CherryPickNoCommit(context.Context, string, ...string) error {
	return g.cherryPickNoCommitErr
}

func (g *keptCommitsStubGit) CommitNoVerify(context.Context, string, string) error {
	return g.commitNoVerifyErr
}

func (g *keptCommitsStubGit) CherryPick(context.Context, string, string) error {
	return g.cherryPickErr
}

func (g *keptCommitsStubGit) RevParse(context.Context, string, string) (string, error) {
	return g.revParseValue, g.revParseErr
}

func (g *keptCommitsStubGit) PushForceWithLeaseHead(context.Context, string, string, string) error {
	return g.pushErr
}

// TestRewriteBranchForKeptCommitsRefusesWhenTheAggregateCherryPickFails
// covers pr_land_keep.go's aggregate-step refusal: also regressed to
// uncovered on unchanged code once the test that used to reach it (via a
// real conflicting cherry-pick) moved to the e2e tier. rewriteBranchForKeptCommits
// takes its Git and runner.Runner ports as plain parameters, so this
// reaches the exact refusal branch through a stub, no real git needed.
func TestRewriteBranchForKeptCommitsRefusesWhenTheAggregateCherryPickFails(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom: conflicting aggregate")
	git := &keptCommitsStubGit{cherryPickNoCommitErr: wantErr}
	plan := keepPlan{steps: []keepStep{{aggregate: true, sources: []SourceCommit{{SHA: "aaa111", Subject: "a"}}}}}

	landed, head, refusal, err := rewriteBranchForKeptCommits(context.Background(), git, nil,
		"/canonical", "acme/app", "refs/heads/candidate", "basesha", plan, PullRequestView{Title: "feat: x"}, nil, "", "", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil (a refusal, not an error)", err)
	}
	if landed != nil || head != "" {
		t.Fatalf("landed=%v head=%q, want both zero on refusal", landed, head)
	}
	if refusal == nil || refusal.code != LandRefusalMergeRejected || !strings.Contains(refusal.reason, wantErr.Error()) {
		t.Fatalf("refusal = %+v, want a LandRefusalMergeRejected naming %v", refusal, wantErr)
	}
}

// TestRewriteBranchForKeptCommitsRefusesWhenAKeptCommitCherryPickFails covers
// the same shape for the non-aggregate (kept) step's own cherry-pick.
func TestRewriteBranchForKeptCommitsRefusesWhenAKeptCommitCherryPickFails(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom: conflicting kept commit")
	git := &keptCommitsStubGit{cherryPickErr: wantErr}
	plan := keepPlan{steps: []keepStep{{sources: []SourceCommit{{SHA: "bbb222", Subject: "b"}}}}}

	_, _, refusal, err := rewriteBranchForKeptCommits(context.Background(), git, nil,
		"/canonical", "acme/app", "refs/heads/candidate", "basesha", plan, PullRequestView{Title: "feat: x"}, nil, "", "", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil (a refusal, not an error)", err)
	}
	if refusal == nil || refusal.code != LandRefusalMergeRejected || !strings.Contains(refusal.reason, wantErr.Error()) {
		t.Fatalf("refusal = %+v, want a LandRefusalMergeRejected naming %v", refusal, wantErr)
	}
}

// TestRewriteBranchForKeptCommitsRefusesWhenTheKeptCommitCannotBuild covers
// rewriteBranchForKeptCommits' own buildAt(...) call site: a refusal from
// buildAt (here, the scratch worktree's own uninferable-build case, since
// the function's os.MkdirTemp scratch directory never actually gets a
// go.mod) must short-circuit the same way a failing build does.
func TestRewriteBranchForKeptCommitsRefusesWhenTheKeptCommitCannotBuild(t *testing.T) {
	t.Parallel()
	git := &keptCommitsStubGit{}
	plan := keepPlan{steps: []keepStep{{sources: []SourceCommit{{SHA: "ccc333", Subject: "c"}}}}}

	_, _, refusal, err := rewriteBranchForKeptCommits(context.Background(), git, nil,
		"/canonical", "acme/app", "refs/heads/candidate", "basesha", plan, PullRequestView{Title: "feat: x"}, nil, "", "", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil (a refusal, not an error)", err)
	}
	if refusal == nil || refusal.code != LandRefusalKeepDoesNotBuild {
		t.Fatalf("refusal = %+v, want LandRefusalKeepDoesNotBuild", refusal)
	}
}

// TestRewriteBranchForKeptCommitsRefusesWhenThePushIsRejected covers the
// final lease-push refusal, reached with an empty plan so the loop above it
// does nothing.
func TestRewriteBranchForKeptCommitsRefusesWhenThePushIsRejected(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom: stale lease")
	git := &keptCommitsStubGit{revParseValue: "deadbeef", pushErr: wantErr}
	view := PullRequestView{Title: "feat: x", Number: 9}
	view.Head.SHA = "deadbeef"

	_, _, refusal, err := rewriteBranchForKeptCommits(context.Background(), git, nil,
		"/canonical", "acme/app", "refs/heads/candidate", "basesha", keepPlan{}, view, nil, "", "", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil (a refusal, not an error)", err)
	}
	if refusal == nil || refusal.code != LandRefusalHeadMoved || !strings.Contains(refusal.reason, wantErr.Error()) {
		t.Fatalf("refusal = %+v, want a LandRefusalHeadMoved naming %v", refusal, wantErr)
	}
}
