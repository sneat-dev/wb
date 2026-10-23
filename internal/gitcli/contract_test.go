//go:build e2e

package gitcli_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/gitcli"
	"github.com/sneat-dev/wb/internal/gitcli/gitclitest"
	"github.com/sneat-dev/wb/internal/runner"
)

// contractGit runs one real git command directly (not through the runner,
// which the runtime guard would block outside the e2e tag anyway) to build
// each contract case's fixture repository.
func contractGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// contractRepo builds a real repository with one commit on main and a
// second on feature, so contract cases have a real ancestor relationship,
// a real current branch and a real HEAD to resolve.
func contractRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	contractGit(t, dir, "init", "-q", "-b", "main")
	contractGit(t, dir, "config", "user.email", "contract@example.test")
	contractGit(t, dir, "config", "user.name", "contract")
	contractGit(t, dir, "commit", "-q", "--allow-empty", "-m", "root")
	contractGit(t, dir, "checkout", "-qb", "feature")
	contractGit(t, dir, "commit", "-q", "--allow-empty", "-m", "feature work")
	return dir
}

// TestContractGitcliCurrentBranchMatchesRealGit runs CurrentBranch against
// a real repository through Client, and asserts a gitclitest.Fake scripted
// with that same real answer behaves identically -- the shape task-8's
// plan calls a contract test.
func TestContractGitcliCurrentBranchMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	realBranch, err := gitcli.New(runner.New()).CurrentBranch(context.Background(), dir)
	if err != nil {
		t.Fatalf("real Client.CurrentBranch: %v", err)
	}
	if realBranch != "feature" {
		t.Fatalf("real Client.CurrentBranch = %q, want %q", realBranch, "feature")
	}

	fake := &gitclitest.Fake{CurrentBranchByDir: map[string]gitclitest.Result{dir: {Value: realBranch}}}
	fakeBranch, err := fake.CurrentBranch(context.Background(), dir)
	if err != nil || fakeBranch != realBranch {
		t.Fatalf("fake.CurrentBranch = (%q, %v), want (%q, nil)", fakeBranch, err, realBranch)
	}
}

// TestContractGitcliRevParseMatchesRealGit runs RevParse for HEAD against a
// real repository, and proves a fake scripted with that same SHA agrees.
func TestContractGitcliRevParseMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	wantSHA := contractGit(t, dir, "rev-parse", "HEAD")

	realSHA, err := gitcli.New(runner.New()).RevParse(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("real Client.RevParse: %v", err)
	}
	if realSHA != wantSHA {
		t.Fatalf("real Client.RevParse = %q, want %q", realSHA, wantSHA)
	}

	fake := &gitclitest.Fake{RevParseByDirAndRev: map[string]gitclitest.Result{dir + "\x00HEAD": {Value: realSHA}}}
	fakeSHA, err := fake.RevParse(context.Background(), dir, "HEAD")
	if err != nil || fakeSHA != realSHA {
		t.Fatalf("fake.RevParse = (%q, %v), want (%q, nil)", fakeSHA, err, realSHA)
	}
}

// TestContractGitcliIsAncestorMatchesRealGitOnAYes proves IsAncestor
// returns true against real git for a real ancestor, and that a fake
// scripted the same way agrees.
func TestContractGitcliIsAncestorMatchesRealGitOnAYes(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	realOK, err := gitcli.New(runner.New()).IsAncestor(context.Background(), dir, "main", "feature")
	if err != nil {
		t.Fatalf("real Client.IsAncestor: %v", err)
	}
	if !realOK {
		t.Fatal("real Client.IsAncestor = false, want true (main is an ancestor of feature)")
	}

	fake := &gitclitest.Fake{IsAncestorByCase: map[string]gitclitest.BoolResult{
		dir + "\x00main\x00feature": {Value: realOK},
	}}
	fakeOK, err := fake.IsAncestor(context.Background(), dir, "main", "feature")
	if err != nil || fakeOK != realOK {
		t.Fatalf("fake.IsAncestor = (%v, %v), want (%v, nil)", fakeOK, err, realOK)
	}
}

// TestContractGitcliIsAncestorMatchesRealGitOnANo is the failure case this
// error kind's fake emulates (task-24's "one case for each error kind a
// fake emulates"): main is not reachable from an unrelated orphan branch,
// so IsAncestor returns false with no error, from both the real client and
// a fake scripted the same way.
func TestContractGitcliIsAncestorMatchesRealGitOnANo(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	contractGit(t, dir, "checkout", "-q", "--orphan", "unrelated")
	contractGit(t, dir, "commit", "-q", "--allow-empty", "-m", "unrelated root")

	realOK, err := gitcli.New(runner.New()).IsAncestor(context.Background(), dir, "main", "unrelated")
	if err != nil {
		t.Fatalf("real Client.IsAncestor: %v", err)
	}
	if realOK {
		t.Fatal("real Client.IsAncestor = true, want false (unrelated shares no history with main)")
	}

	fake := &gitclitest.Fake{IsAncestorByCase: map[string]gitclitest.BoolResult{
		dir + "\x00main\x00unrelated": {Value: realOK},
	}}
	fakeOK, err := fake.IsAncestor(context.Background(), dir, "main", "unrelated")
	if err != nil || fakeOK != realOK {
		t.Fatalf("fake.IsAncestor = (%v, %v), want (%v, nil)", fakeOK, err, realOK)
	}
}

// TestContractGitcliFetchMatchesRealGitAgainstABareRemote proves Fetch
// succeeds against a real bare remote, and that a fake scripted with a nil
// error agrees.
func TestContractGitcliFetchMatchesRealGitAgainstABareRemote(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	remote := t.TempDir()
	contractGit(t, remote, "init", "-q", "--bare")
	contractGit(t, dir, "remote", "add", "origin", remote)
	contractGit(t, dir, "push", "-q", "origin", "main")

	realErr := gitcli.New(runner.New()).Fetch(context.Background(), dir, "origin")
	if realErr != nil {
		t.Fatalf("real Client.Fetch: %v", realErr)
	}

	fake := &gitclitest.Fake{FetchErrByDirAndRemote: map[string]error{dir + "\x00origin": nil}}
	if fakeErr := fake.Fetch(context.Background(), dir, "origin"); fakeErr != nil {
		t.Fatalf("fake.Fetch: %v", fakeErr)
	}
}

// TestContractGitcliFetchMatchesRealGitOnARejectedRemote is the plan's own
// example failure case: fetching a remote that does not exist. Both the
// real client and a fake scripted with that same failure shape report an
// error.
func TestContractGitcliFetchMatchesRealGitOnARejectedRemote(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)

	realErr := gitcli.New(runner.New()).Fetch(context.Background(), dir, "no-such-remote")
	if realErr == nil {
		t.Fatal("real Client.Fetch succeeded fetching a remote that was never configured")
	}

	fake := &gitclitest.Fake{FetchErrByDirAndRemote: map[string]error{dir + "\x00no-such-remote": realErr}}
	if fakeErr := fake.Fetch(context.Background(), dir, "no-such-remote"); fakeErr == nil {
		t.Fatal("fake.Fetch succeeded where the real client failed")
	}
}

// The contract cases below cover the methods gitcli.go adds for
// spec/plans/coverage-to-100 task-17 (internal/orchestrate's Git port): one
// per method, following the same shape as every case above -- run the real
// Client against a real repository, then prove a gitclitest.Fake scripted
// with that same real answer agrees.

func TestContractGitcliWorktreeAddDetachedAndRemoveForceMatchRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	baseSHA := contractGit(t, dir, "rev-parse", "main")
	scratchParent := t.TempDir()
	worktree := scratchParent + "/wt"

	realClient := gitcli.New(runner.New())
	if err := realClient.WorktreeAddDetached(context.Background(), dir, worktree, baseSHA); err != nil {
		t.Fatalf("real Client.WorktreeAddDetached: %v", err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree was not created: %v", err)
	}
	if err := realClient.WorktreeRemoveForce(context.Background(), dir, worktree); err != nil {
		t.Fatalf("real Client.WorktreeRemoveForce: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after WorktreeRemoveForce: %v", err)
	}

	fake := &gitclitest.Fake{
		WorktreeAddDetachedErrByCase: map[string]error{dir + "\x00" + worktree + "\x00" + baseSHA: nil},
		WorktreeRemoveForceErrByCase: map[string]error{dir + "\x00" + worktree: nil},
	}
	if err := fake.WorktreeAddDetached(context.Background(), dir, worktree, baseSHA); err != nil {
		t.Fatalf("fake.WorktreeAddDetached: %v", err)
	}
	if err := fake.WorktreeRemoveForce(context.Background(), dir, worktree); err != nil {
		t.Fatalf("fake.WorktreeRemoveForce: %v", err)
	}
}

// contractCommitWithFile adds path with content on top of dir's checked-out
// branch and returns the new commit's SHA. contractRepo's own two commits
// are --allow-empty (identical trees), which cherry-pick refuses to replay
// as "now empty"; every cherry-pick contract case below needs a commit that
// actually changes a file.
func contractCommitWithFile(t *testing.T, dir, path, content, message string) string {
	t.Helper()
	if err := os.WriteFile(dir+"/"+path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	contractGit(t, dir, "add", path)
	contractGit(t, dir, "commit", "-q", "-m", message)
	return contractGit(t, dir, "rev-parse", "HEAD")
}

func TestContractGitcliCherryPickNoCommitAndCommitNoVerifyMatchRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	contractGit(t, dir, "checkout", "-q", "feature")
	pickSHA := contractCommitWithFile(t, dir, "cherry-no-commit.txt", "content\n", "add cherry-no-commit.txt")
	worktree := t.TempDir()
	contractGit(t, dir, "worktree", "add", "--detach", worktree, "main")

	realClient := gitcli.New(runner.New())
	if err := realClient.CherryPickNoCommit(context.Background(), worktree, pickSHA); err != nil {
		t.Fatalf("real Client.CherryPickNoCommit: %v", err)
	}
	if err := realClient.CommitNoVerify(context.Background(), worktree, "aggregate"); err != nil {
		t.Fatalf("real Client.CommitNoVerify: %v", err)
	}
	newHead := contractGit(t, worktree, "rev-parse", "HEAD")
	if newHead == pickSHA {
		t.Fatal("CommitNoVerify did not write a new commit")
	}

	fake := &gitclitest.Fake{
		CherryPickNoCommitErrByCase: map[string]error{worktree + "\x00" + pickSHA: nil},
		CommitNoVerifyErrByCase:     map[string]error{worktree + "\x00aggregate": nil},
	}
	if err := fake.CherryPickNoCommit(context.Background(), worktree, pickSHA); err != nil {
		t.Fatalf("fake.CherryPickNoCommit: %v", err)
	}
	if err := fake.CommitNoVerify(context.Background(), worktree, "aggregate"); err != nil {
		t.Fatalf("fake.CommitNoVerify: %v", err)
	}
}

func TestContractGitcliCherryPickMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	contractGit(t, dir, "checkout", "-q", "feature")
	pickSHA := contractCommitWithFile(t, dir, "cherry.txt", "content\n", "add cherry.txt")
	worktree := t.TempDir()
	contractGit(t, dir, "worktree", "add", "--detach", worktree, "main")

	realErr := gitcli.New(runner.New()).CherryPick(context.Background(), worktree, pickSHA)
	if realErr != nil {
		t.Fatalf("real Client.CherryPick: %v", realErr)
	}

	fake := &gitclitest.Fake{CherryPickErrByCase: map[string]error{worktree + "\x00" + pickSHA: nil}}
	if fakeErr := fake.CherryPick(context.Background(), worktree, pickSHA); fakeErr != nil {
		t.Fatalf("fake.CherryPick: %v", fakeErr)
	}
}

func TestContractGitcliPushForceWithLeaseHeadMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	remote := t.TempDir()
	contractGit(t, remote, "init", "-q", "--bare")
	contractGit(t, dir, "remote", "add", "origin", remote)
	contractGit(t, dir, "checkout", "-q", "main")
	contractGit(t, dir, "push", "-q", "origin", "main")
	leaseSHA := contractGit(t, dir, "rev-parse", "main")
	contractGit(t, dir, "commit", "-q", "--allow-empty", "-m", "another commit")

	realErr := gitcli.New(runner.New()).PushForceWithLeaseHead(context.Background(), dir, "main", leaseSHA)
	if realErr != nil {
		t.Fatalf("real Client.PushForceWithLeaseHead: %v", realErr)
	}

	fake := &gitclitest.Fake{PushForceWithLeaseHeadErrByCase: map[string]error{dir + "\x00main\x00" + leaseSHA: nil}}
	if fakeErr := fake.PushForceWithLeaseHead(context.Background(), dir, "main", leaseSHA); fakeErr != nil {
		t.Fatalf("fake.PushForceWithLeaseHead: %v", fakeErr)
	}
}

func TestContractGitcliFetchRefsMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	remote := t.TempDir()
	contractGit(t, remote, "init", "-q", "--bare")
	contractGit(t, dir, "remote", "add", "origin", remote)
	contractGit(t, dir, "push", "-q", "origin", "main", "feature")

	realErr := gitcli.New(runner.New()).FetchRefs(context.Background(), dir, "origin", "main", "feature")
	if realErr != nil {
		t.Fatalf("real Client.FetchRefs: %v", realErr)
	}

	fake := &gitclitest.Fake{FetchRefsErrByCase: map[string]error{dir + "\x00origin\x00main,feature": nil}}
	if fakeErr := fake.FetchRefs(context.Background(), dir, "origin", "main", "feature"); fakeErr != nil {
		t.Fatalf("fake.FetchRefs: %v", fakeErr)
	}
}

func TestContractGitcliRevListReverseRangeMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	baseSHA := contractGit(t, dir, "rev-parse", "main")
	headSHA := contractGit(t, dir, "rev-parse", "feature")

	realOut, err := gitcli.New(runner.New()).RevListReverseRange(context.Background(), dir, baseSHA, headSHA)
	if err != nil {
		t.Fatalf("real Client.RevListReverseRange: %v", err)
	}
	if realOut != headSHA {
		t.Fatalf("real Client.RevListReverseRange = %q, want %q", realOut, headSHA)
	}

	fake := &gitclitest.Fake{RevListReverseRangeByCase: map[string]gitclitest.Result{
		dir + "\x00" + baseSHA + "\x00" + headSHA: {Value: realOut},
	}}
	fakeOut, err := fake.RevListReverseRange(context.Background(), dir, baseSHA, headSHA)
	if err != nil || fakeOut != realOut {
		t.Fatalf("fake.RevListReverseRange = (%q, %v), want (%q, nil)", fakeOut, err, realOut)
	}
}

func TestContractGitcliRemotePushURLMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	remote := t.TempDir()
	contractGit(t, remote, "init", "-q", "--bare")
	contractGit(t, dir, "remote", "add", "origin", remote)

	realURL, err := gitcli.New(runner.New()).RemotePushURL(context.Background(), dir, "origin")
	if err != nil {
		t.Fatalf("real Client.RemotePushURL: %v", err)
	}
	if realURL != remote {
		t.Fatalf("real Client.RemotePushURL = %q, want %q", realURL, remote)
	}

	fake := &gitclitest.Fake{RemotePushURLByDirAndRemote: map[string]gitclitest.Result{dir + "\x00origin": {Value: realURL}}}
	fakeURL, err := fake.RemotePushURL(context.Background(), dir, "origin")
	if err != nil || fakeURL != realURL {
		t.Fatalf("fake.RemotePushURL = (%q, %v), want (%q, nil)", fakeURL, err, realURL)
	}
}

func TestContractGitcliStatusPorcelainMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	if err := os.WriteFile(dir+"/untracked.txt", []byte("x"), 0o600); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	realStatus, err := gitcli.New(runner.New()).StatusPorcelain(context.Background(), dir)
	if err != nil {
		t.Fatalf("real Client.StatusPorcelain: %v", err)
	}
	if realStatus == "" {
		t.Fatal("real Client.StatusPorcelain reported clean with an untracked file present")
	}

	fake := &gitclitest.Fake{StatusPorcelainByDir: map[string]gitclitest.Result{dir: {Value: realStatus}}}
	fakeStatus, err := fake.StatusPorcelain(context.Background(), dir)
	if err != nil || fakeStatus != realStatus {
		t.Fatalf("fake.StatusPorcelain = (%q, %v), want (%q, nil)", fakeStatus, err, realStatus)
	}
}

func TestContractGitcliBranchShowCurrentMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)

	realBranch, err := gitcli.New(runner.New()).BranchShowCurrent(context.Background(), dir)
	if err != nil {
		t.Fatalf("real Client.BranchShowCurrent: %v", err)
	}
	if realBranch != "feature" {
		t.Fatalf("real Client.BranchShowCurrent = %q, want %q", realBranch, "feature")
	}

	fake := &gitclitest.Fake{BranchShowCurrentByDir: map[string]gitclitest.Result{dir: {Value: realBranch}}}
	fakeBranch, err := fake.BranchShowCurrent(context.Background(), dir)
	if err != nil || fakeBranch != realBranch {
		t.Fatalf("fake.BranchShowCurrent = (%q, %v), want (%q, nil)", fakeBranch, err, realBranch)
	}
}

// TestContractGitcliMergeBaseIsAncestorStrictMatchesRealGitOnANotAncestor is
// the case that makes this method's contract worth proving separately from
// IsAncestor's: real git's exit status 1 must still surface as a non-nil
// error here.
func TestContractGitcliMergeBaseIsAncestorStrictMatchesRealGitOnANotAncestor(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)

	realErr := gitcli.New(runner.New()).MergeBaseIsAncestorStrict(context.Background(), dir, "feature", "main")
	if realErr == nil {
		t.Fatal("real Client.MergeBaseIsAncestorStrict succeeded where feature is not an ancestor of main")
	}

	fake := &gitclitest.Fake{MergeBaseIsAncestorStrictErrByCase: map[string]error{dir + "\x00feature\x00main": realErr}}
	if fakeErr := fake.MergeBaseIsAncestorStrict(context.Background(), dir, "feature", "main"); fakeErr == nil {
		t.Fatal("fake.MergeBaseIsAncestorStrict succeeded where the real client failed")
	}
}

func TestContractGitcliBranchSetUpstreamToMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	remote := t.TempDir()
	contractGit(t, remote, "init", "-q", "--bare")
	contractGit(t, dir, "remote", "add", "origin", remote)
	contractGit(t, dir, "push", "-q", "origin", "feature")

	realErr := gitcli.New(runner.New()).BranchSetUpstreamTo(context.Background(), dir, "origin/feature", "feature")
	if realErr != nil {
		t.Fatalf("real Client.BranchSetUpstreamTo: %v", realErr)
	}
	tracked := contractGit(t, dir, "rev-parse", "--abbrev-ref", "feature@{upstream}")
	if tracked != "origin/feature" {
		t.Fatalf("upstream = %q, want %q", tracked, "origin/feature")
	}

	fake := &gitclitest.Fake{BranchSetUpstreamToErrByCase: map[string]error{dir + "\x00origin/feature\x00feature": nil}}
	if fakeErr := fake.BranchSetUpstreamTo(context.Background(), dir, "origin/feature", "feature"); fakeErr != nil {
		t.Fatalf("fake.BranchSetUpstreamTo: %v", fakeErr)
	}
}

func TestContractGitcliMergeTreeWriteTreeAndShowTreeFormatMatchRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	mainSHA := contractGit(t, dir, "rev-parse", "main")
	featureSHA := contractGit(t, dir, "rev-parse", "feature")

	realClient := gitcli.New(runner.New())
	writtenTree, err := realClient.MergeTreeWriteTree(context.Background(), dir, featureSHA, mainSHA)
	if err != nil {
		t.Fatalf("real Client.MergeTreeWriteTree: %v", err)
	}
	headTree, err := realClient.ShowTreeFormat(context.Background(), dir, featureSHA)
	if err != nil {
		t.Fatalf("real Client.ShowTreeFormat: %v", err)
	}
	if writtenTree != headTree {
		t.Fatalf("merge-tree of feature onto main = %q, want feature's own tree %q (no divergent change)", writtenTree, headTree)
	}

	fake := &gitclitest.Fake{
		MergeTreeWriteTreeByCase: map[string]gitclitest.Result{dir + "\x00" + featureSHA + "\x00" + mainSHA: {Value: writtenTree}},
		ShowTreeFormatByCase:     map[string]gitclitest.Result{dir + "\x00" + featureSHA: {Value: headTree}},
	}
	fakeTree, err := fake.MergeTreeWriteTree(context.Background(), dir, featureSHA, mainSHA)
	if err != nil || fakeTree != writtenTree {
		t.Fatalf("fake.MergeTreeWriteTree = (%q, %v), want (%q, nil)", fakeTree, err, writtenTree)
	}
	fakeHeadTree, err := fake.ShowTreeFormat(context.Background(), dir, featureSHA)
	if err != nil || fakeHeadTree != headTree {
		t.Fatalf("fake.ShowTreeFormat = (%q, %v), want (%q, nil)", fakeHeadTree, err, headTree)
	}
}

func TestContractGitcliCommitObjectExistsMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	featureSHA := contractGit(t, dir, "rev-parse", "feature")

	realClient := gitcli.New(runner.New())
	exists, err := realClient.CommitObjectExists(context.Background(), dir, featureSHA)
	if err != nil || !exists {
		t.Fatalf("real Client.CommitObjectExists = (%v, %v), want (true, nil) for a commit that exists", exists, err)
	}
	absent, err := realClient.CommitObjectExists(context.Background(), dir, "0000000000000000000000000000000000000000")
	if err != nil || absent {
		t.Fatalf("real Client.CommitObjectExists = (%v, %v), want (false, nil) for a SHA that does not exist", absent, err)
	}

	fake := &gitclitest.Fake{CommitObjectExistsByCase: map[string]gitclitest.BoolResult{dir + "\x00" + featureSHA: {Value: true}}}
	fakeExists, err := fake.CommitObjectExists(context.Background(), dir, featureSHA)
	if err != nil || !fakeExists {
		t.Fatalf("fake.CommitObjectExists = (%v, %v), want (true, nil)", fakeExists, err)
	}
}

func TestContractGitcliConfigRegexpMatchesMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	contractGit(t, dir, "config", "example.key", "value")

	realClient := gitcli.New(runner.New())
	matched, err := realClient.ConfigRegexpMatches(context.Background(), dir, "^example\\.key$")
	if err != nil {
		t.Fatalf("real Client.ConfigRegexpMatches: %v", err)
	}
	if !matched {
		t.Fatal("real Client.ConfigRegexpMatches = false for a key that is set")
	}
	unmatched, err := realClient.ConfigRegexpMatches(context.Background(), dir, "^example\\.absent$")
	if err != nil {
		t.Fatalf("real Client.ConfigRegexpMatches: %v", err)
	}
	if unmatched {
		t.Fatal("real Client.ConfigRegexpMatches = true for a key that is not set")
	}

	fake := &gitclitest.Fake{ConfigRegexpMatchesByCase: map[string]gitclitest.BoolResult{
		dir + "\x00^example\\.key$": {Value: true},
	}}
	fakeMatched, err := fake.ConfigRegexpMatches(context.Background(), dir, "^example\\.key$")
	if err != nil || fakeMatched != true {
		t.Fatalf("fake.ConfigRegexpMatches = (%v, %v), want (true, nil)", fakeMatched, err)
	}
}
