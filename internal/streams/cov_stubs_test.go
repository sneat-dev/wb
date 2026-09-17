package streams

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// stCovGitDefaultBranchErr fails the default-branch read.
type stCovGitDefaultBranchErr struct {
	*fakeGit
	err error
}

func (git stCovGitDefaultBranchErr) DefaultBranch(context.Context, string) (string, error) {
	return "", git.err
}

// stCovGitRemoteHeadErr fails the remote-head read.
type stCovGitRemoteHeadErr struct {
	*fakeGit
	err error
}

func (git stCovGitRemoteHeadErr) RemoteHead(context.Context, string, string) (string, bool, error) {
	return "", false, git.err
}

// stCovGitLocalHeadErr fails the local-head read.
type stCovGitLocalHeadErr struct {
	*fakeGit
	err error
}

func (git stCovGitLocalHeadErr) LocalHead(context.Context, string) (string, error) {
	return "", git.err
}

// stCovGitDirtyErr fails the worktree inspection.
type stCovGitDirtyErr struct {
	*fakeGit
	err error
}

func (git stCovGitDirtyErr) DirtyPaths(context.Context, string) ([]string, error) {
	return nil, git.err
}

// stCovGitLocalBranchHeadErr fails or empties the local stream-ref read.
type stCovGitLocalBranchHeadErr struct {
	*fakeGit
	sha   string
	found bool
	err   error
}

func (git stCovGitLocalBranchHeadErr) LocalBranchHead(context.Context, string, string) (string, bool, error) {
	return git.sha, git.found, git.err
}

// stCovGitIsAncestorErr fails the ancestry comparison.
type stCovGitIsAncestorErr struct {
	*fakeGit
	err error
}

func (git stCovGitIsAncestorErr) IsAncestor(context.Context, string, string, string) (bool, error) {
	return false, git.err
}

// stCovHubViewErr fails a pull-request re-read.
type stCovHubViewErr struct {
	*fakeHub
	err error
}

func (hub stCovHubViewErr) PullRequest(context.Context, string, int) (PullRequest, bool, error) {
	return PullRequest{}, false, hub.err
}

// stCovHubRetargetErr fails a retarget.
type stCovHubRetargetErr struct {
	*fakeHub
	err error
}

func (hub stCovHubRetargetErr) RetargetPullRequest(context.Context, string, int, string) error {
	return hub.err
}

// stCovLockingWorktrees makes the per-stream lock unopenable at the moment the
// worktree path reports creation, which is the first write that follows it.
type stCovLockingWorktrees struct {
	*fakeWorktrees
	lockDir string
}

func (worktrees *stCovLockingWorktrees) Create(ctx context.Context, task, branch string, repositories []string) ([]CreatedWorktree, error) {
	created, err := worktrees.fakeWorktrees.Create(ctx, task, branch, repositories)
	if err != nil {
		return created, err
	}
	if err := stCovBlockLockAt(worktrees.lockDir); err != nil {
		return nil, err
	}
	return created, nil
}

// stCovLockingCreateHub blocks the per-stream lock while a draft pull request
// is being opened, which is after the push has already been recorded.
type stCovLockingCreateHub struct {
	*fakeHub
	lockDir string
}

func (hub *stCovLockingCreateHub) CreateDraftPullRequest(ctx context.Context, dir, base, head, title, body string) (PullRequest, error) {
	if err := stCovBlockLockAt(hub.lockDir); err != nil {
		return PullRequest{}, err
	}
	return hub.fakeHub.CreateDraftPullRequest(ctx, dir, base, head, title, body)
}

// stCovLockingBranchHub blocks the per-stream lock while an existing pull
// request is being discovered.
type stCovLockingBranchHub struct {
	*fakeHub
	lockDir string
}

func (hub *stCovLockingBranchHub) PullRequestForBranch(ctx context.Context, dir, branch string) (PullRequest, bool, error) {
	if err := stCovBlockLockAt(hub.lockDir); err != nil {
		return PullRequest{}, false, err
	}
	return hub.fakeHub.PullRequestForBranch(ctx, dir, branch)
}

// stCovBlockLockAt replaces whatever occupies the lock path with a directory,
// so the next per-stream Update fails to open its lock.
func stCovBlockLockAt(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(path, 0o700)
}

// stCovBlockStreamLock replaces a stream's per-stream lock file with a
// directory, so the next Update fails while a Load still succeeds.
func stCovBlockStreamLock(t *testing.T, store *Store, name string) {
	t.Helper()
	if err := stCovBlockLockAt(filepath.Join(store.Dir(name), "stream.lock")); err != nil {
		t.Fatal(err)
	}
}
