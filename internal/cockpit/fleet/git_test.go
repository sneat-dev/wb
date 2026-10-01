package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
)

// fakeGit writes an executable script standing in for the Git binary.
func fakeGit(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(realTempDir(t), "fakegit")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestGitEnvironmentIsAnAllowListPlusTheHardeningSettings requires nothing
// from the daemon's environment but PATH, HOME, TMPDIR, LANG and LC_*, and the
// settings that keep Git read-only, local and non-interactive.
func TestGitEnvironmentIsAnAllowListPlusTheHardeningSettings(t *testing.T) {
	t.Parallel()
	parent := []string{
		"PATH=/bin", "HOME=/home/x", "TMPDIR=/tmp", "LANG=C", "LC_ALL=C", "GIT_DIR=/elsewhere", "GIT_WORK_TREE=/w", "GIT_NAMESPACE=n",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=/bin/evil", "GIT_SSH_COMMAND=evil", "SECRET=token", "GIT_OPTIONAL_LOCKS=1",
	}
	environment := gitEnvironment(parent)
	for _, forbidden := range []string{"GIT_DIR=/elsewhere", "GIT_WORK_TREE=/w", "GIT_NAMESPACE=n", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_SSH_COMMAND=evil", "SECRET=token", "GIT_OPTIONAL_LOCKS=1"} {
		if slices.Contains(environment, forbidden) {
			t.Errorf("the Git environment passes through %s", forbidden)
		}
	}
	for _, required := range []string{
		"PATH=/bin", "HOME=/home/x", "TMPDIR=/tmp", "LANG=C", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
	} {
		if !slices.Contains(environment, required) {
			t.Errorf("the Git environment lacks %s: %v", required, environment)
		}
	}
	arguments := strings.Join(gitArguments("/repo", []string{"for-each-ref"}), " ")
	for _, required := range []string{"--no-optional-locks", "-C /repo", "protocol.allow=never", "protocol.ext.allow=never", "protocol.file.allow=never", "protocol.git.allow=never", "protocol.ssh.allow=never", "protocol.http.allow=never", "protocol.https.allow=never", "core.fsmonitor=false", "core.hooksPath=/dev/null", "uploadpack.allowFilter=false", "for-each-ref"} {
		if !strings.Contains(arguments, required) {
			t.Errorf("the Git arguments lack %s: %s", required, arguments)
		}
	}
}

// TestHostileRepositoryConfigRunsNothingWhenItIsSnapshotted gives a repository
// a config naming a filesystem monitor, a hooks path, an ssh command and a
// promisor remote, with the README's blob missing so a read would lazily fetch
// it; every read the snapshot makes runs none of them.
func TestHostileRepositoryConfigRunsNothingWhenItIsSnapshotted(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(realTempDir(t), "ran")
	script := filepath.Join(realTempDir(t), "hostile.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
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

// TestInheritedGitEnvironmentDoesNotChangeResults sets GIT_DIR, GIT_WORK_TREE
// and a GIT_CONFIG_* pair that names a program in this process's environment:
// the snapshot still reads the repository it was asked about and runs nothing.
func TestInheritedGitEnvironmentDoesNotChangeResults(t *testing.T) {
	marker := filepath.Join(realTempDir(t), "ran")
	script := filepath.Join(realTempDir(t), "inherited.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
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

// TestGitCommandThatSpawnsAChildHoldingStdoutIsReapedOnTimeout runs a stand-in
// Git that leaves a child holding its standard output, and requires the call
// to return soon after its timeout, with the child killed too.
func TestGitCommandThatSpawnsAChildHoldingStdoutIsReapedOnTimeout(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(realTempDir(t), "child.pid")
	binary := fakeGit(t, "sleep 30 &\necho $! > "+pidFile+".tmp\nmv "+pidFile+".tmp "+pidFile+"\nsleep 30")
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	returned := make(chan error, 1)
	go func() {
		_, err := gitOutput(ctx, binary, t.TempDir(), "for-each-ref")
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
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the child process %d outlived its command", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDefaultBranchComesFromOriginsHEADThenTheCheckedOutBranch covers origin's
// symbolic HEAD, the checked-out branch and a detached HEAD with neither.
func TestDefaultBranchComesFromOriginsHEADThenTheCheckedOutBranch(t *testing.T) {
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

// TestReadmeRefusesWhatGitReportsOddly drives the README read with a stand-in
// Git that prints a malformed tree entry, an unparsable size, a size over the
// cap and a blob longer than its size said.
func TestReadmeRefusesWhatGitReportsOddly(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	entry := "printf '100644 blob " + sha + "\\tREADME.md\\0'"
	for name, test := range map[string]struct {
		script string
		want   error
	}{
		"a malformed tree entry": {`case " $* " in *" ls-tree "*) printf 'garbage\0';; esac`, errGit},
		"an entry with a bad id": {`case " $* " in *" ls-tree "*) printf '100644 blob xyz\tREADME.md\0';; esac`, errGit},
		"a non-hex object id":    {`case " $* " in *" ls-tree "*) printf '100644 blob ` + strings.Repeat("g", 40) + `\tREADME.md\0';; esac`, errGit},
		"an unparsable size":     {`case " $* " in *" ls-tree "*) ` + entry + `;; *" -s "*) echo lots;; esac`, errGit},
		"a size over the cap":    {`case " $* " in *" ls-tree "*) ` + entry + `;; *" -s "*) echo 2000000;; esac`, errReadmeTooLarge},
		"a blob over its size":   {`case " $* " in *" ls-tree "*) ` + entry + `;; *" -s "*) echo 5;; *) head -c 1100000 /dev/zero;; esac`, errReadmeTooLarge},
		"a failing command":      {`exit 128`, errGit},
		"a failing listing":      {`case " $* " in *" show-ref "*) ;; *) exit 128;; esac`, errGit},
		"a missing branch":       {`exit 1`, errReadmeAbsent},
	} {
		collectors := LocalCollectors{Git: fakeGit(t, test.script)}
		if _, err := collectors.Readme(t.Context(), discover.Repo{Path: t.TempDir()}, "main"); !errors.Is(err, test.want) {
			t.Errorf("%s: %v, want %v", name, err, test.want)
		}
	}
}

// TestGitVersionFloorIsTwoFortyFive covers the version parser.
func TestGitVersionFloorIsTwoFortyFive(t *testing.T) {
	t.Parallel()
	for output, want := range map[string]bool{
		"git version 2.45.0":                 true,
		"git version 2.54.0 (Apple Git-157)": true,
		"git version 3.0.1":                  true,
		"git version 2.44.9":                 false,
		"git version 1.99.0":                 false,
		"git version 2":                      false,
		"git version x.y.z":                  false,
		"not git":                            false,
		"":                                   false,
	} {
		if got := gitVersionUsable(output); got != want {
			t.Errorf("gitVersionUsable(%q) = %v, want %v", output, got, want)
		}
	}
}

// TestGitUsableAsksTheGitBinary covers the real Git, an old stand-in and a
// failing one.
func TestGitUsableAsksTheGitBinary(t *testing.T) {
	t.Parallel()
	if !(LocalCollectors{}).GitUsable(t.Context()) {
		t.Skip("the Git on this machine is older than 2.45")
	}
	if (LocalCollectors{Git: fakeGit(t, "echo git version 2.30.0")}).GitUsable(t.Context()) {
		t.Error("an old Git was usable")
	}
	if (LocalCollectors{Git: fakeGit(t, "exit 3")}).GitUsable(t.Context()) {
		t.Error("a failing Git was usable")
	}
}

// TestCappedBufferNeverHoldsMoreThanItsCap requires a write past the cap to
// fail without buffering any of it.
func TestCappedBufferNeverHoldsMoreThanItsCap(t *testing.T) {
	t.Parallel()
	buffer := &cappedBuffer{max: 4}
	if _, err := buffer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("de")); !errors.Is(err, errGitOutputTooLarge) || buffer.data.Len() != 3 || !buffer.exceeded {
		t.Errorf("a write past the cap = %v with %d bytes held", err, buffer.data.Len())
	}
	if _, err := gitOutputLimited(t.Context(), fakeGit(t, "head -c 100000 /dev/zero"), t.TempDir(), 10, "x"); !errors.Is(err, errGitOutputTooLarge) {
		t.Errorf("a command past its output cap = %v", err)
	}
	if _, err := gitOutput(t.Context(), filepath.Join(t.TempDir(), "no-such-git"), t.TempDir(), "x"); !errors.Is(err, errGit) {
		t.Errorf("a Git binary that cannot start = %v", err)
	}
	var exit error = exitError{code: 1}
	if !errors.Is(exit, errGit) || exit.Error() != errGit.Error() {
		t.Error("an exit error is not errGit")
	}
}

// TestReadmeOfAMissingBranchIsAbsentAndOtherFailuresAreNot uses a real clone:
// a branch that does not exist is an absent README; a stand-in Git that fails
// differently is a failure.
func TestReadmeOfAMissingBranchIsAbsentAndOtherFailuresAreNot(t *testing.T) {
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
