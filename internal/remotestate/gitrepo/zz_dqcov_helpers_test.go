package gitrepo

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// dqCovBlockedLockClonePath returns a ClonePath whose parent directory is a
// regular file, so acquireCloneLock cannot create the sibling lock directory.
// Every public Provider method that takes the clone lock must surface that as
// an error, and this is the shared fixture for those cases.
func dqCovBlockedLockClonePath(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(blocker, "wb-state")
}

// dqCovSymlink creates link -> target or skips the test when the platform
// cannot create symlinks (Windows without developer mode / privilege). It is
// used only for filesystem-failure fixtures that have no portable equivalent.
func dqCovSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// dqCovChmodDirForcesError is a fixture helper: it chmods a directory to 0o000
// so that reading or removing entries inside it fails with a permission error.
// On Windows directory mode bits do not gate enumeration, so the caller skips.
func dqCovChmodDir(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not deny access on windows")
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

// dqCovPreClone creates a real clone of origin at clonePath before any
// Provider call, so a test can plant files that must exist before the first
// Publish/Fetch runs against that directory.
func dqCovPreClone(t *testing.T, origin, clonePath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, filepath.Dir(clonePath), "clone", "-q", origin, clonePath)
}

// dqCovEmptyBareOrigin creates a bare repo with no branches at all, the shape
// `gh repo create` leaves behind.
func dqCovEmptyBareOrigin(t *testing.T) string {
	t.Helper()
	setGitIdentity(t)
	origin := t.TempDir()
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")
	testenv.ConfigureGitAutoMaintenanceOff(t, origin)
	return origin
}

// dqCovCommitFile writes content at a store-relative path in a throwaway clone
// of origin and pushes it to ref, synthesizing another writer's commit.
func dqCovPushFileToRef(t *testing.T, origin, relative string, content []byte, ref, message string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "dq-prep")
	gitIn(t, t.TempDir(), "clone", "-q", origin, work)
	abs := filepath.Join(work, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-q", "-m", message)
	gitIn(t, work, "push", "-q", "origin", "HEAD:"+ref)
}

// dqCovPushSymlinkToRef commits a symlink at a store-relative path and pushes
// it to ref. Symlink creation is skipped where unsupported.
func dqCovPushSymlinkToRef(t *testing.T, origin, relative, target, ref, message string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "dq-prep-link")
	gitIn(t, t.TempDir(), "clone", "-q", origin, work)
	abs := filepath.Join(work, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	dqCovSymlink(t, target, abs)
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-q", "-m", message)
	gitIn(t, work, "push", "-q", "origin", "HEAD:"+ref)
}

// dqCovPushSymlinkReplacingToRef is dqCovPushSymlinkToRef for a path that
// already exists in the store: it removes the file before linking.
func dqCovPushSymlinkReplacingToRef(t *testing.T, origin, relative, target, ref, message string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "dq-prep-replace-link")
	gitIn(t, t.TempDir(), "clone", "-q", origin, work)
	abs := filepath.Join(work, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	dqCovSymlink(t, target, abs)
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-q", "-m", message)
	gitIn(t, work, "push", "-q", "origin", "HEAD:"+ref)
}

// dqCovInstallClientMutatingRejectHook rigs origin so its first received push
// is rejected after promoting promoteSHA onto main and after mutating the
// client clone's branch config (which dqCovUnsetUpstreamConfig describes).
// promoteSHA must be a descendant of the base main so the local push itself
// would otherwise be a fast-forward.
func dqCovInstallClientMutatingRejectHook(t *testing.T, origin, promoteSHA, clientClone string) {
	t.Helper()
	hooksDir := filepath.Join(origin, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
set -e
gitdir="$(git rev-parse --git-dir)"
marker="$gitdir/wb-test-hook-fired"
if [ ! -f "$marker" ]; then
  touch "$marker"
  env -u GIT_DIR -u GIT_WORK_TREE git -C %s config --unset branch.main.remote || true
  env -u GIT_DIR -u GIT_WORK_TREE git -C %s config --unset branch.main.merge || true
  env -u GIT_QUARANTINE_PATH git update-ref refs/heads/main %s
  exit 1
fi
exit 0
`, dqCovShellQuote(clientClone), dqCovShellQuote(clientClone), promoteSHA)
	if err := testenv.WriteExecutableFile(filepath.Join(hooksDir, "pre-receive"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// dqCovInstallRemoveClientBranchHook rigs origin so a received push succeeds
// on the server but leaves the client's checked-out branch ref deleted, which
// makes the post-push HeadSHA step fail.
func dqCovInstallRemoveClientBranchHook(t *testing.T, origin, clientClone string) {
	t.Helper()
	hooksDir := filepath.Join(origin, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nenv -u GIT_DIR -u GIT_WORK_TREE -u GIT_QUARANTINE_PATH git -C %s update-ref -d refs/heads/main || true\nexit 0\n", dqCovShellQuote(clientClone))
	if err := testenv.WriteExecutableFile(filepath.Join(hooksDir, "pre-receive"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// dqCovInstallRejectTwiceHook rigs origin so its first two received pushes are
// each rejected after promoting a different commit onto main; later pushes are
// accepted. promote2 must be a descendant of promote1.
func dqCovInstallRejectTwiceHook(t *testing.T, origin, promote1, promote2 string) {
	t.Helper()
	hooksDir := filepath.Join(origin, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
set -e
gitdir="$(git rev-parse --git-dir)"
marker="$gitdir/wb-test-hook-count"
n=0
if [ -f "$marker" ]; then n="$(cat "$marker")"; fi
n=$((n+1))
echo "$n" > "$marker"
if [ "$n" = "1" ]; then
  env -u GIT_QUARANTINE_PATH git update-ref refs/heads/main %s
  exit 1
fi
if [ "$n" = "2" ]; then
  env -u GIT_QUARANTINE_PATH git update-ref refs/heads/main %s
  exit 1
fi
exit 0
`, promote1, promote2)
	if err := testenv.WriteExecutableFile(filepath.Join(hooksDir, "pre-receive"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// dqCovShellQuote single-quotes a path for /bin/sh.
func dqCovShellQuote(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += `'\''`
			continue
		}
		out += string(r)
	}
	return out + "'"
}

// dqCovInstallFailingCommitHook installs a client-side pre-commit hook in
// clonePath that always fails, so gitops.AddCommit's staging succeeds but the
// commit does not.
func dqCovInstallFailingCommitHook(t *testing.T, clonePath string) {
	t.Helper()
	hooksDir := filepath.Join(clonePath, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := testenv.WriteExecutableFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\necho 'dqCov: commit refused' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
