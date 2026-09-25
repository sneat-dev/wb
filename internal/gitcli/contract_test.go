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

// TestContractGitcliRunMatchesRealGit runs an arbitrary subcommand
// (`remote get-url origin`) against a real repository and proves a fake
// scripted with that same answer agrees.
func TestContractGitcliRunMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	remote := t.TempDir()
	contractGit(t, remote, "init", "-q", "--bare")
	contractGit(t, dir, "remote", "add", "origin", remote)

	realOut, err := gitcli.New(runner.New()).Run(context.Background(), dir, "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("real Client.Run: %v", err)
	}
	if realOut != remote {
		t.Fatalf("real Client.Run = %q, want %q", realOut, remote)
	}

	fake := &gitclitest.Fake{RunByDirAndArgv: map[string]gitclitest.Result{
		dir + "\x00remote\x00get-url\x00origin": {Value: realOut},
	}}
	fakeOut, err := fake.Run(context.Background(), dir, "remote", "get-url", "origin")
	if err != nil || fakeOut != realOut {
		t.Fatalf("fake.Run = (%q, %v), want (%q, nil)", fakeOut, err, realOut)
	}
}

// TestContractGitcliAtomicRenameRefsMatchesRealGit renames a real
// repository's checked-out branch and proves a fake scripted with the same
// (nil) outcome agrees.
func TestContractGitcliAtomicRenameRefsMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	contractGit(t, dir, "checkout", "-q", "main")
	sha := contractGit(t, dir, "rev-parse", "main")

	realErr := gitcli.New(runner.New()).AtomicRenameRefs(context.Background(), dir, "main", "trunk", sha)
	if realErr != nil {
		t.Fatalf("real Client.AtomicRenameRefs: %v", realErr)
	}
	// AtomicRenameRefs leaves HEAD detached at expected: it is the ref
	// transaction only, and reattaching is a separate step (AttachHead)
	// its caller runs after checkpointing, matching the original
	// unported behaviour this method replaced.
	if head := contractGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); head != "HEAD" {
		t.Fatalf("abbrev-ref HEAD after rename = %q, want %q (still detached)", head, "HEAD")
	}
	if resolved := contractGit(t, dir, "rev-parse", "HEAD"); resolved != sha {
		t.Fatalf("HEAD after rename = %q, want %q", resolved, sha)
	}
	if exists := contractGit(t, dir, "rev-parse", "--verify", "--quiet", "refs/heads/trunk"); exists != sha {
		t.Fatalf("refs/heads/trunk = %q, want %q", exists, sha)
	}
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", "refs/heads/main").CombinedOutput(); err == nil {
		t.Fatalf("refs/heads/main still exists after rename: %s", out)
	}

	fake := &gitclitest.Fake{AtomicRenameRefsErrByCase: map[string]error{
		dir + "\x00main\x00trunk\x00" + sha: nil,
	}}
	if fakeErr := fake.AtomicRenameRefs(context.Background(), dir, "main", "trunk", sha); fakeErr != nil {
		t.Fatalf("fake.AtomicRenameRefs: %v", fakeErr)
	}
}

// TestContractGitcliAtomicRenameRefsMatchesRealGitOnAStaleExpectedSHA is the
// failure case: the expected SHA no longer matches the branch tip, so the
// detach checkout itself fails against real git, and a fake scripted with
// that same failure shape agrees.
func TestContractGitcliAtomicRenameRefsMatchesRealGitOnAStaleExpectedSHA(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	contractGit(t, dir, "checkout", "-q", "main")

	realErr := gitcli.New(runner.New()).AtomicRenameRefs(context.Background(), dir, "main", "trunk", "0000000000000000000000000000000000000000")
	if realErr == nil {
		t.Fatal("real Client.AtomicRenameRefs succeeded against a SHA that does not exist")
	}

	fake := &gitclitest.Fake{AtomicRenameRefsErrByCase: map[string]error{
		dir + "\x00main\x00trunk\x000000000000000000000000000000000000000000": realErr,
	}}
	if fakeErr := fake.AtomicRenameRefs(context.Background(), dir, "main", "trunk", "0000000000000000000000000000000000000000"); fakeErr == nil {
		t.Fatal("fake.AtomicRenameRefs succeeded where the real client failed")
	}
}

// TestContractGitcliAttachHeadMatchesRealGit points a real repository's
// detached HEAD back at a local branch and proves a fake scripted with the
// same outcome agrees.
func TestContractGitcliAttachHeadMatchesRealGit(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)
	contractGit(t, dir, "checkout", "-q", "--detach", "main")

	realErr := gitcli.New(runner.New()).AttachHead(context.Background(), dir, "main")
	if realErr != nil {
		t.Fatalf("real Client.AttachHead: %v", realErr)
	}
	if branch := contractGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "main" {
		t.Fatalf("HEAD after attach = %q, want %q", branch, "main")
	}

	fake := &gitclitest.Fake{AttachHeadErrByDirAndDestination: map[string]error{dir + "\x00main": nil}}
	if fakeErr := fake.AttachHead(context.Background(), dir, "main"); fakeErr != nil {
		t.Fatalf("fake.AttachHead: %v", fakeErr)
	}
}

// TestContractGitcliRefExistsMatchesRealGitOnAYes proves RefExists returns
// true for a real ref, and that a fake scripted the same way agrees.
func TestContractGitcliRefExistsMatchesRealGitOnAYes(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)

	realOK, err := gitcli.New(runner.New()).RefExists(context.Background(), dir, "refs/heads/main")
	if err != nil {
		t.Fatalf("real Client.RefExists: %v", err)
	}
	if !realOK {
		t.Fatal("real Client.RefExists = false, want true (refs/heads/main exists)")
	}

	fake := &gitclitest.Fake{RefExistsByDirAndRef: map[string]gitclitest.BoolResult{
		dir + "\x00refs/heads/main": {Value: realOK},
	}}
	fakeOK, err := fake.RefExists(context.Background(), dir, "refs/heads/main")
	if err != nil || fakeOK != realOK {
		t.Fatalf("fake.RefExists = (%v, %v), want (%v, nil)", fakeOK, err, realOK)
	}
}

// TestContractGitcliRefExistsMatchesRealGitOnANo is the failure case this
// error kind's fake emulates: a ref that does not exist reports false with
// no error, from both the real client and a fake scripted the same way.
func TestContractGitcliRefExistsMatchesRealGitOnANo(t *testing.T) {
	t.Parallel()
	dir := contractRepo(t)

	realOK, err := gitcli.New(runner.New()).RefExists(context.Background(), dir, "refs/heads/no-such-branch")
	if err != nil {
		t.Fatalf("real Client.RefExists: %v", err)
	}
	if realOK {
		t.Fatal("real Client.RefExists = true, want false (refs/heads/no-such-branch does not exist)")
	}

	fake := &gitclitest.Fake{RefExistsByDirAndRef: map[string]gitclitest.BoolResult{
		dir + "\x00refs/heads/no-such-branch": {Value: realOK},
	}}
	fakeOK, err := fake.RefExists(context.Background(), dir, "refs/heads/no-such-branch")
	if err != nil || fakeOK != realOK {
		t.Fatalf("fake.RefExists = (%v, %v), want (%v, nil)", fakeOK, err, realOK)
	}
}
