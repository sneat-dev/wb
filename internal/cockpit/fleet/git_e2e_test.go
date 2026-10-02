//go:build e2e

package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/testenv"
)

// fakeGit writes an executable script standing in for the Git binary.
func fakeGit(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(realTempDir(t), "fakegit")
	if err := testenv.WriteExecutableFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestE2EHostileRepositoryConfigRunsNothingWhenItIsSnapshotted gives a repository
// a config naming a filesystem monitor, a hooks path, an ssh command and a
// promisor remote, with the README's blob missing so a read would lazily fetch
// it; every read the snapshot makes runs none of them.
func TestE2EHostileRepositoryConfigRunsNothingWhenItIsSnapshotted(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(realTempDir(t), "ran")
	script := filepath.Join(realTempDir(t), "hostile.sh")
	if err := testenv.WriteExecutableFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newRealFleet(t)
	hostileConfig(t, f.clone, script)
	blob := strings.TrimSpace(gitIn(t, f.clone, "rev-parse", "HEAD:README.md"))
	if err := os.Remove(filepath.Join(f.clone, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	collectors := LocalCollectors{ProjectsRoot: f.root, Home: t.TempDir()}
	repo := discover.Repo{Org: "acme", Name: "widgets", Path: f.clone}
	if _, err := collectors.Branches(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	collectors.DefaultBranch(t.Context(), repo)
	if _, err := collectors.Readme(t.Context(), repo, "main"); err == nil {
		t.Error("a README whose blob is missing was read")
	}
	if _, err := collectors.Worktrees(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	snapshotter := New(Options{Machine: testMachine, Collectors: collectors.Collectors(nil)})
	refreshAndSettle(t, snapshotter)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("the repository's own configuration ran a program")
	}
}

// TestE2EInheritedGitEnvironmentDoesNotChangeResults sets GIT_DIR, GIT_WORK_TREE
// and a GIT_CONFIG_* pair that names a program in this process's environment:
// the snapshot still reads the repository it was asked about and runs nothing.
//
//nolint:paralleltest // calls t.Setenv to put GIT_DIR, GIT_WORK_TREE and GIT_CONFIG_* in the inherited environment, which Go's testing package forbids combined with t.Parallel
func TestE2EInheritedGitEnvironmentDoesNotChangeResults(t *testing.T) {
	marker := filepath.Join(realTempDir(t), "ran")
	script := filepath.Join(realTempDir(t), "inherited.sh")
	if err := testenv.WriteExecutableFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newRealFleet(t)
	elsewhere := realTempDir(t)
	gitIn(t, elsewhere, "init", "--initial-branch=other")
	t.Setenv("GIT_DIR", filepath.Join(elsewhere, ".git"))
	t.Setenv("GIT_WORK_TREE", elsewhere)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", script)
	collectors := LocalCollectors{ProjectsRoot: f.root, Home: t.TempDir()}
	repo := discover.Repo{Org: "acme", Name: "widgets", Path: f.clone}
	refs, err := collectors.Branches(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, ref := range refs {
		names[ref.Name] = true
	}
	if !names["feature/one"] || names["other"] {
		t.Errorf("branches = %v, want the repository's own", names)
	}
	if branch := collectors.DefaultBranch(t.Context(), repo); branch != "main" {
		t.Errorf("default branch = %q, want main", branch)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("an inherited GIT_CONFIG_* ran a program")
	}
}

// TestE2EGitCommandThatSpawnsAChildHoldingStdoutIsReapedOnTimeout runs a stand-in
// Git that leaves a child holding its standard output, and requires the call
// to return soon after its timeout, with the child killed too.
func TestE2EGitCommandThatSpawnsAChildHoldingStdoutIsReapedOnTimeout(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(realTempDir(t), "child.pid")
	binary := fakeGit(t, "sleep 30 &\necho $! > "+pidFile+".tmp\nmv "+pidFile+".tmp "+pidFile+"\nsleep 30")
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	returned := make(chan error, 1)
	go func() {
		_, err := readGit(ctx, nil, binary, t.TempDir(), "for-each-ref")
		returned <- err
	}()
	// The timeout is a cancellation once the stand-in has started its child.
	// Fail fast with the real error if the command ends before the child is seen, and allow a loaded machine 30s.
	childDeadline := time.Now().Add(30 * time.Second)
	var early error
	for seen := false; !seen; {
		select {
		case early = <-returned:
			t.Fatalf("the command returned before its child started: %v", early)
		default:
		}
		if _, err := os.Stat(pidFile); err == nil {
			seen = true
		} else if time.Now().After(childDeadline) {
			t.Fatal("timed out waiting for the child to start")
		} else {
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(50 * time.Millisecond)
	started := time.Now()
	cancel()
	if err := <-returned; !errors.Is(err, errGit) {
		t.Fatalf("a cancelled command = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the call took %v to return after its context ended", elapsed)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid := 0
	for _, digit := range strings.TrimSpace(string(data)) {
		pid = pid*10 + int(digit-'0')
	}
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the child process %d outlived its command", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestE2EDefaultBranchComesFromOriginsHEADThenTheCheckedOutBranch covers origin's
// symbolic HEAD, the checked-out branch and a detached HEAD with neither.
func TestE2EDefaultBranchComesFromOriginsHEADThenTheCheckedOutBranch(t *testing.T) {
	t.Parallel()
	f := newRealFleet(t)
	collectors := LocalCollectors{}
	repo := discover.Repo{Path: f.clone}
	gitIn(t, f.clone, "branch", "-m", "main", "trunk")
	if got := collectors.DefaultBranch(t.Context(), repo); got != "trunk" {
		t.Errorf("default branch from the checked-out branch = %q, want trunk", got)
	}
	gitIn(t, f.clone, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	if got := collectors.DefaultBranch(t.Context(), repo); got != "main" {
		t.Errorf("default branch from origin's HEAD = %q, want main", got)
	}
	gitIn(t, f.clone, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	gitIn(t, f.clone, "checkout", "--detach")
	if got := collectors.DefaultBranch(t.Context(), repo); got != "" {
		t.Errorf("default branch of a detached clone with no origin HEAD = %q, want none", got)
	}
}

// TestE2EGitUsableAsksTheGitBinary covers the real Git, an old stand-in and a
// failing one.
func TestE2EGitUsableAsksTheGitBinary(t *testing.T) {
	t.Parallel()
	if usable, err := (LocalCollectors{}).GitUsable(t.Context()); !usable || err != nil {
		t.Skip("the Git on this machine is older than 2.45")
	}
	if usable, err := (LocalCollectors{Git: fakeGit(t, "echo git version 2.30.0")}).GitUsable(t.Context()); usable || err != nil {
		t.Error("an old Git was usable")
	}
	if usable, err := (LocalCollectors{Git: fakeGit(t, "exit 3")}).GitUsable(t.Context()); usable || err == nil {
		t.Error("a failing Git was usable")
	}
}

// TestE2EGitOutputNeverHoldsMoreThanItsCap requires output past the cap to fail
// with nothing returned, and a command that cannot start, or exits non-zero,
// to fail with a message that carries no text.
func TestE2EGitOutputNeverHoldsMoreThanItsCap(t *testing.T) {
	t.Parallel()
	if _, err := gitOutputLimited(t.Context(), nil, fakeGit(t, "head -c 100000 /dev/zero"), t.TempDir(), 10, "x"); !errors.Is(err, errGitOutputTooLarge) {
		t.Errorf("a command past its output cap = %v", err)
	}
	if _, err := readGit(t.Context(), nil, filepath.Join(t.TempDir(), "no-such-git"), t.TempDir(), "x"); !errors.Is(err, errGit) || !errors.Is(err, errCommandMissing) {
		t.Errorf("a Git binary that cannot start = %v", err)
	}
	var exit error = exitError{code: 1}
	if !errors.Is(exit, errGit) || exit.Error() != errGit.Error() {
		t.Error("an exit error is not errGit")
	}
}

// TestE2EReadmeOfAMissingBranchIsAbsentAndOtherFailuresAreNot uses a real clone:
// a branch that does not exist is an absent README; a stand-in Git that fails
// differently is a failure.
func TestE2EReadmeOfAMissingBranchIsAbsentAndOtherFailuresAreNot(t *testing.T) {
	t.Parallel()
	f := newRealFleet(t)
	repo := discover.Repo{Path: f.clone}
	if _, err := (LocalCollectors{}).Readme(t.Context(), repo, "no-such-branch"); !errors.Is(err, errReadmeAbsent) {
		t.Errorf("README of a missing branch = %v, want absent", err)
	}
	if _, err := (LocalCollectors{Git: fakeGit(t, "exit 2")}).Readme(t.Context(), repo, "main"); !errors.Is(err, errGit) || errors.Is(err, errReadmeAbsent) {
		t.Errorf("README when Git fails = %v, want a failure", err)
	}
}

// TestE2EAGitCommandThatIgnoresSIGTERMIsKilledWithItsGroupOnTimeout runs a
// stand-in Git that ignores SIGTERM (as does the child it starts, which inherits
// the setting): the group is ended only by the SIGKILL the runner escalates to,
// so the call must still return within its budget, as errGit and not as Git's
// "no", and the child must be gone.
func TestE2EAGitCommandThatIgnoresSIGTERMIsKilledWithItsGroupOnTimeout(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(realTempDir(t), "child.pid")
	binary := fakeGit(t, "trap '' TERM\nsleep 30 &\necho $! > "+pidFile+".tmp\nmv "+pidFile+".tmp "+pidFile+"\nwait")
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	returned := make(chan error, 1)
	go func() {
		_, err := readGit(ctx, nil, binary, t.TempDir(), "for-each-ref")
		returned <- err
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		select {
		case err := <-returned:
			t.Fatalf("the command returned before its child started: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the child to start")
		}
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	cancel()
	err := <-returned
	if !errors.Is(err, errGit) || notFound(err) {
		t.Fatalf("a command that ignored SIGTERM = %v, want errGit and not Git's no", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the call took %v to return after its context ended", elapsed)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(3 * time.Second); processAlive(pid); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the child process %d outlived its command", pid)
		}
	}
}
