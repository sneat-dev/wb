package streamsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stCovRequireGit skips only where Git itself is absent: the production port
// under test is a real-exec layer, so a fixture without Git cannot exist.
func stCovRequireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// stCovScratchRepo is a local repository with one commit on main and no
// remote, so the purely local half of the ExecGit port is driven against Git
// itself and never against a network.
func stCovScratchRepo(t *testing.T) string {
	t.Helper()
	stCovRequireGit(t)
	root := t.TempDir()
	runGit(t, "", "init", "--initial-branch=main", root)
	// The port under test commits with its own environment, so the repository
	// carries an explicit identity rather than relying on git autodetecting one.
	runGit(t, root, "config", "user.email", "wb@example.test")
	runGit(t, root, "config", "user.name", "WB Test")
	commitFile(t, root, "base.txt", "base\n", "feat: base")
	return root
}

func stCovHead(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

func TestExecGitCommitAllReportsWhetherAnythingWasCommitted(t *testing.T) {
	t.Parallel()
	root := stCovScratchRepo(t)
	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()

	sha, ok, err := git.CommitAll(ctx, root, "feat: nothing changed")
	if err != nil {
		t.Fatalf("CommitAll on a clean tree: %v", err)
	}
	if ok || sha != "" {
		t.Fatalf("CommitAll on a clean tree = %q, %t; want no commit", sha, ok)
	}
	if after := stCovHead(t, root); after == "" {
		t.Fatal("HEAD did not resolve")
	}

	if err := os.WriteFile(filepath.Join(root, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, ok, err = git.CommitAll(ctx, root, "feat: the work")
	if err != nil || !ok {
		t.Fatalf("CommitAll with a change = %q, %t, %v; want a commit", sha, ok, err)
	}
	if sha != stCovHead(t, root) {
		t.Fatalf("CommitAll reported %s, HEAD is %s", sha, stCovHead(t, root))
	}
	if clean, err := git.IsClean(ctx, root); err != nil || !clean {
		t.Fatalf("IsClean after committing = %t, %v; want clean", clean, err)
	}
}

func TestExecGitIsCleanReportsUncommittedWork(t *testing.T) {
	t.Parallel()
	root := stCovScratchRepo(t)
	git := ExecGit{Timeout: time.Minute}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, err := git.IsClean(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Fatal("IsClean reported a worktree with an untracked file as clean")
	}
}

func TestExecGitBranchCheckoutAndDeleteRoundTrip(t *testing.T) {
	t.Parallel()
	root := stCovScratchRepo(t)
	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()
	head := stCovHead(t, root)

	if branch, err := git.CurrentBranch(ctx, root); err != nil || branch != "main" {
		t.Fatalf("CurrentBranch = %q, %v; want main", branch, err)
	}
	if err := git.CreateBranch(ctx, root, "topic", head); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if resolved, err := git.Head(ctx, root, "topic"); err != nil || resolved != head {
		t.Fatalf("Head(topic) = %q, %v; want %s", resolved, err, head)
	}
	// --force semantics: re-pointing an existing branch must not fail.
	if err := git.CreateBranch(ctx, root, "topic", head); err != nil {
		t.Fatalf("CreateBranch over an existing branch: %v", err)
	}
	if err := git.Checkout(ctx, root, "topic"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if branch, err := git.CurrentBranch(ctx, root); err != nil || branch != "topic" {
		t.Fatalf("CurrentBranch after checkout = %q, %v; want topic", branch, err)
	}
	if err := git.Checkout(ctx, root, "main"); err != nil {
		t.Fatalf("Checkout back to main: %v", err)
	}
	if err := git.DeleteBranch(ctx, root, "topic"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if _, err := git.Head(ctx, root, "topic"); err == nil {
		t.Fatal("Head(topic) resolved after DeleteBranch; the branch was not removed")
	}
}

func TestExecGitResetHardAndRestoreToDiscardUncommittedWork(t *testing.T) {
	t.Parallel()
	root := stCovScratchRepo(t)
	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()
	committed := stCovHead(t, root)

	tracked := filepath.Join(root, "base.txt")
	if err := os.WriteFile(tracked, []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git.ResetHard(ctx, root, committed); err != nil {
		t.Fatalf("ResetHard: %v", err)
	}
	contents, err := os.ReadFile(tracked)
	if err != nil || string(contents) != "base\n" {
		t.Fatalf("ResetHard left %q (err %v); want the committed contents", contents, err)
	}

	// RestoreTo must also remove untracked residue a failed bump leaves behind.
	if err := os.WriteFile(tracked, []byte("modified again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	residue := filepath.Join(root, "go.sum.lock")
	if err := os.WriteFile(residue, []byte("residue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git.RestoreTo(ctx, root, committed); err != nil {
		t.Fatalf("RestoreTo: %v", err)
	}
	if contents, err := os.ReadFile(tracked); err != nil || string(contents) != "base\n" {
		t.Fatalf("RestoreTo left %q (err %v)", contents, err)
	}
	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Fatalf("RestoreTo left untracked residue behind (stat err %v)", err)
	}
}

func TestExecGitRebaseReportsConflictingPathsAndAbortRebaseRecovers(t *testing.T) {
	t.Parallel()
	root := stCovScratchRepo(t)
	commitFile(t, root, "shared.txt", "base\n", "feat: add shared")
	runGit(t, root, "checkout", "-b", "topic")
	commitFile(t, root, "shared.txt", "topic\n", "feat: topic change")
	topicHead := stCovHead(t, root)
	runGit(t, root, "checkout", "main")
	commitFile(t, root, "shared.txt", "main\n", "feat: main change")

	git := ExecGit{Timeout: time.Minute}
	conflicts, err := git.Rebase(context.Background(), root, "topic", "main")
	if err != nil {
		t.Fatalf("Rebase on a conflicting branch returned an error instead of the conflict: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0] != "shared.txt" {
		t.Fatalf("conflicts = %v, want [shared.txt]", conflicts)
	}
	if err := git.AbortRebase(context.Background(), root); err != nil {
		t.Fatalf("AbortRebase: %v", err)
	}
	if branch, err := git.CurrentBranch(context.Background(), root); err != nil || branch != "topic" {
		t.Fatalf("CurrentBranch after abort = %q, %v; want topic", branch, err)
	}
	if head := stCovHead(t, root); head != topicHead {
		t.Fatalf("topic head after abort = %s, want the pre-rebase %s", head, topicHead)
	}
	contents, err := os.ReadFile(filepath.Join(root, "shared.txt"))
	if err != nil || string(contents) != "topic\n" {
		t.Fatalf("shared.txt after abort = %q (err %v); want the pre-rebase contents", contents, err)
	}
}

func TestExecGitRebaseFailsWithoutAPathWhenGitCannotRebaseAtAll(t *testing.T) {
	t.Parallel()
	root := stCovScratchRepo(t)
	git := ExecGit{Timeout: time.Minute}
	conflicts, err := git.Rebase(context.Background(), root, "main", "no-such-upstream")
	if err == nil {
		t.Fatalf("Rebase onto a nonexistent upstream = %v, want an error", conflicts)
	}
	if !strings.Contains(err.Error(), "without reporting a conflicting path") {
		t.Fatalf("error = %v; want a refusal that names the absent conflict", err)
	}
}

func TestExecGitCherryPickAppliesAndAbortsAConflictingPick(t *testing.T) {
	t.Parallel()
	root := stCovScratchRepo(t)
	commitFile(t, root, "shared.txt", "base\n", "feat: shared base")
	runGit(t, root, "checkout", "-b", "topic")
	commitFile(t, root, "shared.txt", "topic\n", "feat: topic change")
	conflicting := stCovHead(t, root)
	commitFile(t, root, "other.txt", "other\n", "feat: independent change")
	independent := stCovHead(t, root)
	runGit(t, root, "checkout", "main")
	commitFile(t, root, "shared.txt", "main\n", "feat: main change")

	git := ExecGit{Timeout: time.Minute}
	if err := git.CherryPick(context.Background(), root, independent); err != nil {
		t.Fatalf("CherryPick of an independent commit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "other.txt")); err != nil {
		t.Fatalf("cherry-picked file is absent: %v", err)
	}
	// The successful pick advanced HEAD, so this is the head the aborted pick
	// must restore.
	beforeConflict := stCovHead(t, root)

	err := git.CherryPick(context.Background(), root, conflicting)
	if err == nil {
		t.Fatal("CherryPick of a conflicting commit reported success")
	}
	// The pick failed and was aborted: HEAD is back where it started and no
	// cherry-pick state remains to strand the tree.
	if head := stCovHead(t, root); head != beforeConflict {
		t.Fatalf("HEAD after the aborted pick = %s, want the pre-pick %s", head, beforeConflict)
	}
	command := exec.Command("git", "rev-parse", "-q", "--verify", "CHERRY_PICK_HEAD")
	command.Dir = root
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("CHERRY_PICK_HEAD still resolves to %q; the conflicting pick was not aborted", strings.TrimSpace(string(output)))
	}
}

func TestExecGitCommitsAheadCountsAgainstTheUpstreamItHas(t *testing.T) {
	t.Parallel()
	fixture := newRemoteStreamFixture(t)
	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()

	ahead, err := git.CommitsAhead(ctx, fixture.local, "stream/fixture", "origin/stream/fixture")
	if err != nil || ahead != 0 {
		t.Fatalf("CommitsAhead on equal refs = %d, %v; want 0", ahead, err)
	}
	commitFile(t, fixture.local, "ahead.txt", "ahead\n", "feat: ahead")
	ahead, err = git.CommitsAhead(ctx, fixture.local, "stream/fixture", "origin/stream/fixture")
	if err != nil || ahead != 1 {
		t.Fatalf("CommitsAhead after one commit = %d, %v; want 1", ahead, err)
	}
	// No remote counterpart at all: every reachable commit is unpushed.
	ahead, err = git.CommitsAhead(ctx, fixture.local, "stream/fixture", "origin/absent")
	if err != nil || ahead != 2 {
		t.Fatalf("CommitsAhead without a remote counterpart = %d, %v; want 2", ahead, err)
	}
	// Both refs unresolvable: the count cannot be established, and the port
	// reports zero rather than failing the whole sync.
	ahead, err = git.CommitsAhead(ctx, fixture.local, "no-such-branch", "origin/absent")
	if err != nil || ahead != 0 {
		t.Fatalf("CommitsAhead on an unknown branch = %d, %v; want 0, nil", ahead, err)
	}
}

func TestExecGitFastForwardToRemoteAcceptsEqualHeads(t *testing.T) {
	t.Parallel()
	fixture := newRemoteStreamFixture(t)
	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()
	local := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "stream/fixture"))
	if err := git.Fetch(ctx, fixture.local); err != nil {
		t.Fatal(err)
	}
	head, present, advanced, err := git.FastForwardToRemote(ctx, fixture.local, "stream/fixture", "origin/stream/fixture")
	if err != nil {
		t.Fatalf("FastForwardToRemote on equal heads: %v", err)
	}
	if !present || advanced || head != local {
		t.Fatalf("equal heads = head %q present=%t advanced=%t; want the unchanged local head", head, present, advanced)
	}
}

func TestExecGitFastForwardToRemoteAcceptsALocalAheadBranch(t *testing.T) {
	t.Parallel()
	fixture := newRemoteStreamFixture(t)
	commitFile(t, fixture.local, "local.txt", "local\n", "feat: local ahead")
	local := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "stream/fixture"))
	remote := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "origin/stream/fixture"))

	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()
	if err := git.Fetch(ctx, fixture.local); err != nil {
		t.Fatal(err)
	}
	head, present, advanced, err := git.FastForwardToRemote(ctx, fixture.local, "stream/fixture", "origin/stream/fixture")
	if err != nil {
		t.Fatalf("FastForwardToRemote with the remote as ancestor: %v", err)
	}
	if !present || advanced {
		t.Fatalf("remote-ancestor state = head %q present=%t advanced=%t; want an untouched local-ahead branch", head, present, advanced)
	}
	if head != remote {
		t.Fatalf("reported remote head = %s, want %s", head, remote)
	}
	if after := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "stream/fixture")); after != local {
		t.Fatalf("local head = %s, want unchanged %s", after, local)
	}
}

func TestExecGitPushWithLeasePublishesAndVerifiesTheRef(t *testing.T) {
	t.Parallel()
	fixture := newRemoteStreamFixture(t)
	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()
	commitFile(t, fixture.local, "pushed.txt", "pushed\n", "feat: push me")
	local := stCovHead(t, fixture.local)

	// An empty expected head is the first-publication case: plain
	// --set-upstream, still verified against origin afterwards.
	published, err := git.PushWithLease(ctx, fixture.local, "stream/fixture", "")
	if err != nil {
		t.Fatalf("PushWithLease for a first publication: %v", err)
	}
	if published != local {
		t.Fatalf("published head = %s, want %s", published, local)
	}
	if remote := strings.TrimSpace(runGit(t, fixture.local, "ls-remote", "--heads", "origin", "refs/heads/stream/fixture")); !strings.HasPrefix(remote, local) {
		t.Fatalf("origin/stream/fixture = %q, want %s", remote, local)
	}

	// A matching lease lets an ordinary fast-forward through.
	commitFile(t, fixture.local, "more.txt", "more\n", "feat: more")
	next := stCovHead(t, fixture.local)
	published, err = git.PushWithLease(ctx, fixture.local, "stream/fixture", local)
	if err != nil {
		t.Fatalf("PushWithLease with a matching lease: %v", err)
	}
	if published != next {
		t.Fatalf("published head = %s, want %s", published, next)
	}
}

func TestExecGitPushWithLeaseRefusesAStaleLease(t *testing.T) {
	t.Parallel()
	fixture := newRemoteStreamFixture(t)
	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()
	stale := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "stream/fixture"))

	commitFile(t, fixture.local, "first.txt", "first\n", "feat: first")
	if _, err := git.PushWithLease(ctx, fixture.local, "stream/fixture", stale); err != nil {
		t.Fatalf("first leased push: %v", err)
	}
	onRemote := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "origin/stream/fixture"))

	// Rewrite the local commit so only a force could publish it, then present
	// the now-stale lease: the remote advanced behind the caller's back and
	// the push must be refused rather than discarding that advance.
	runGit(t, fixture.local, "commit", "--amend", "-m", "feat: first, rewritten")
	_, err := git.PushWithLease(ctx, fixture.local, "stream/fixture", stale)
	if err == nil {
		t.Fatal("PushWithLease with a stale lease reported success")
	}
	if !strings.Contains(err.Error(), "push") {
		t.Fatalf("stale-lease error = %v, want a push failure", err)
	}
	remote := strings.TrimSpace(runGit(t, fixture.local, "ls-remote", "--heads", "origin", "refs/heads/stream/fixture"))
	if fields := strings.Fields(remote); len(fields) != 2 || fields[0] != onRemote {
		t.Fatalf("origin advanced to %q despite the stale lease, want %s", remote, onRemote)
	}
}

func stCovWriteExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func stCovFakeBin(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake toolchain helpers are POSIX shell scripts")
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestExecBumperRequiredReadsTheLowestConsumerDeclaration(t *testing.T) {
	t.Parallel()
	bumper := ExecBumper{Timeout: time.Minute}
	ctx := context.Background()
	library := Library{Name: "acme.test/library", Target: "v1.2.3", Ecosystem: "go"}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.test/root\n\ngo 1.22\n\nrequire acme.test/library v0.9.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backend", "go.mod"),
		[]byte("module example.test/backend\n\ngo 1.22\n\nrequire (\n\tacme.test/library v1.1.0\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	version, found, err := bumper.Required(ctx, root, library)
	if err != nil || !found {
		t.Fatalf("Required = %q, %t, %v; want the declared version", version, found, err)
	}
	if version != "v0.9.0" {
		t.Fatalf("Required = %q, want the lowest declared version v0.9.0", version)
	}

	if _, found, err := bumper.Required(ctx, root, Library{Name: "acme.test/absent", Ecosystem: "go"}); err != nil || found {
		t.Fatalf("Required for an undeclared library = found %t, %v; want false", found, err)
	}
}

func TestExecBumperRequiredReadsNpmDeclarations(t *testing.T) {
	t.Parallel()
	bumper := ExecBumper{Timeout: time.Minute}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"),
		[]byte(`{"name":"consumer","dependencies":{"@acme/core":"^2.4.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	version, found, err := bumper.Required(context.Background(), root, Library{Name: "@acme/core", Ecosystem: "npm"})
	if err != nil || !found || version != "^2.4.0" {
		t.Fatalf("Required = %q, %t, %v; want ^2.4.0", version, found, err)
	}
}

func TestExecBumperRequiredReportsAnUnreadableConsumerManifest(t *testing.T) {
	t.Parallel()
	bumper := ExecBumper{Timeout: time.Minute}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bumper.Required(context.Background(), root, Library{Name: "@acme/core", Ecosystem: "npm"}); err == nil {
		t.Fatal("Required on an unparseable manifest reported no error")
	}
}

// The Go bump must run the toolchain that refreshes go.sum in EVERY module of
// the consumer and must pin GOWORK=off so a workspace cannot resolve a local
// library tree into an unpublished go.sum.
func TestExecBumperApplyGoRunsGetAndTidyInEveryModule(t *testing.T) {
	bin := stCovFakeBin(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/root\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backend", "go.mod"), []byte("module example.test/backend\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "go.log")
	stCovWriteExecutable(t, bin, "go", `#!/bin/sh
printf '%s|%s|%s\n' "$PWD" "$*" "$GOWORK" >> "$STCOV_GO_LOG"
case "$STCOV_GO_FAIL" in
  get) case "$1 $2" in "get "*) exit 1 ;; esac ;;
  tidy) case "$1 $2" in "mod tidy") exit 1 ;; esac ;;
esac
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STCOV_GO_LOG", log)

	bumper := ExecBumper{Timeout: time.Minute}
	library := Library{Name: "acme.test/library", Target: "v1.2.3", Ecosystem: "go"}
	if err := bumper.Apply(context.Background(), root, library); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	// The child reports its physical working directory, which on macOS differs
	// from the /var symlink t.TempDir hands out.
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		physical + "|get acme.test/library@v1.2.3|off",
		physical + "|mod tidy|off",
		filepath.Join(physical, "backend") + "|get acme.test/library@v1.2.3|off",
		filepath.Join(physical, "backend") + "|mod tidy|off",
	}
	if got := strings.Split(strings.TrimSpace(string(contents)), "\n"); !stCovEqualLines(got, want) {
		t.Fatalf("go invocations =\n%v\nwant\n%v", got, want)
	}
}

func TestExecBumperApplyGoReportsAFailedGetOrTidy(t *testing.T) {
	bin := stCovFakeBin(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/root\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stCovWriteExecutable(t, bin, "go", "#!/bin/sh\ncase \"$STCOV_GO_FAIL\" in\n  get) case \"$1 $2\" in \"get \"*) echo 'get failed' >&2; exit 1 ;; esac ;;\n  tidy) case \"$1 $2\" in \"mod tidy\") echo 'tidy failed' >&2; exit 1 ;; esac ;;\nesac\nexit 0\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	bumper := ExecBumper{Timeout: time.Minute}
	library := Library{Name: "acme.test/library", Target: "v1.2.3", Ecosystem: "go"}

	t.Setenv("STCOV_GO_FAIL", "get")
	if err := bumper.Apply(context.Background(), root, library); err == nil {
		t.Fatal("Apply reported success when `go get` failed")
	}
	t.Setenv("STCOV_GO_FAIL", "tidy")
	if err := bumper.Apply(context.Background(), root, library); err == nil {
		t.Fatal("Apply reported success when `go mod tidy` failed")
	}
}

func TestExecBumperApplyNpmUsesTheLockfileOwner(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		manager string
		lock    bool
	}{
		{name: "pnpm when the lockfile is present", manager: "pnpm", lock: true},
		{name: "npm when there is no pnpm lockfile", manager: "npm", lock: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := stCovFakeBin(t)
			root := t.TempDir()
			log := filepath.Join(t.TempDir(), "npm.log")
			for _, manager := range []string{"npm", "pnpm"} {
				stCovWriteExecutable(t, bin, manager,
					"#!/bin/sh\nprintf '%s\\n' \""+manager+" $*\" >> \"$STCOV_NPM_LOG\"\n")
			}
			if test.lock {
				if err := os.WriteFile(filepath.Join(root, "pnpm-lock.yaml"), []byte("lockfileVersion: '9.0'\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("STCOV_NPM_LOG", log)

			bumper := ExecBumper{Timeout: time.Minute}
			if err := bumper.Apply(context.Background(), root, Library{Name: "@acme/core", Ecosystem: "npm"}); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			contents, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(contents)); got != test.manager+" install --lockfile-only" {
				t.Fatalf("npm invocations = %q, want %q", got, test.manager+" install --lockfile-only")
			}
		})
	}
}

func TestExecBumperApplyNpmReportsAFailedInstall(t *testing.T) {
	bin := stCovFakeBin(t)
	root := t.TempDir()
	stCovWriteExecutable(t, bin, "npm", "#!/bin/sh\necho 'install failed' >&2\nexit 1\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	bumper := ExecBumper{Timeout: time.Minute}
	if err := bumper.Apply(context.Background(), root, Library{Name: "@acme/core", Ecosystem: "npm"}); err == nil {
		t.Fatal("Apply reported success when the package manager failed")
	}
}

func TestExecBumperApplyRefusesAnUnsupportedEcosystem(t *testing.T) {
	t.Parallel()
	bumper := ExecBumper{Timeout: time.Minute}
	err := bumper.Apply(context.Background(), t.TempDir(), Library{Name: "acme-crate", Ecosystem: "cargo"})
	if err == nil {
		t.Fatal("Apply accepted an unsupported ecosystem")
	}
	if !strings.Contains(err.Error(), "cargo") {
		t.Fatalf("error = %v, want it to name the unsupported ecosystem", err)
	}
}

func TestSyncRunBoundedReportsATimeoutRatherThanHanging(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is not installed")
	}
	start := time.Now()
	_, err := runBounded(context.Background(), 50*time.Millisecond, t.TempDir(), nil, "sleep", "30")
	if err == nil {
		t.Fatal("a command that outlived its bound reported success")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("runBounded took %s; the bound was not enforced", elapsed)
	}
}

func TestExecGitRebaseFastForwardsACleanBranch(t *testing.T) {
	t.Parallel()
	root := stCovScratchRepo(t)
	runGit(t, root, "checkout", "-b", "topic")
	commitFile(t, root, "topic.txt", "topic\n", "feat: topic work")
	runGit(t, root, "checkout", "main")
	commitFile(t, root, "main.txt", "main\n", "feat: main work")

	git := ExecGit{Timeout: time.Minute}
	conflicts, err := git.Rebase(context.Background(), root, "topic", "main")
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}
	// The rebase really replayed the topic commit onto main: both files are
	// now reachable from topic.
	for _, name := range []string{"topic.txt", "main.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s is absent after the rebase: %v", name, err)
		}
	}
}

func TestExecGitRebaseRefusesAnUnknownBranch(t *testing.T) {
	t.Parallel()
	root := stCovScratchRepo(t)
	git := ExecGit{Timeout: time.Minute}
	conflicts, err := git.Rebase(context.Background(), root, "no-such-branch", "main")
	if err == nil {
		t.Fatalf("Rebase of an unknown branch = %v, want an error", conflicts)
	}
	if !strings.Contains(err.Error(), "no-such-branch") {
		t.Fatalf("error = %v, want it to name the unknown branch", err)
	}
}

func TestExecGitFastForwardToRemoteReportsAHeadItCannotRead(t *testing.T) {
	t.Parallel()
	fixture := newRemoteStreamFixture(t)
	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()
	if err := git.Fetch(ctx, fixture.local); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := git.FastForwardToRemote(ctx, fixture.local, "no-such-branch", "origin/stream/fixture")
	if err == nil {
		t.Fatal("FastForwardToRemote accepted a local branch that does not exist")
	}
	if !strings.Contains(err.Error(), "no-such-branch") {
		t.Fatalf("error = %v, want it to name the unreadable branch", err)
	}
}

func TestExecGitFastForwardToRemoteFailsClosedOutsideARepository(t *testing.T) {
	t.Parallel()
	stCovRequireGit(t)
	git := ExecGit{Timeout: time.Minute}
	_, _, _, err := git.FastForwardToRemote(context.Background(), t.TempDir(), "stream/x", "origin/stream/x")
	if err == nil {
		t.Fatal("FastForwardToRemote inspected a directory that is not a repository without error")
	}
	if !strings.Contains(err.Error(), "origin/stream/x") {
		t.Fatalf("error = %v, want it to name the remote it could not inspect", err)
	}
}

func TestExecGitFastForwardToRemoteRefusesToOverwriteUntrackedWork(t *testing.T) {
	t.Parallel()
	fixture := newRemoteStreamFixture(t)
	runGit(t, fixture.other, "checkout", "stream/fixture")
	commitFile(t, fixture.other, "remote.txt", "remote\n", "feat: remote advance")
	runGit(t, fixture.other, "push", "origin", "stream/fixture")
	localHead := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "stream/fixture"))

	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()
	if err := git.Fetch(ctx, fixture.local); err != nil {
		t.Fatal(err)
	}
	// An untracked file the fast-forward would have to create is exactly what
	// `merge --ff-only` refuses to clobber.
	if err := os.WriteFile(filepath.Join(fixture.local, "remote.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head, present, advanced, err := git.FastForwardToRemote(ctx, fixture.local, "stream/fixture", "origin/stream/fixture")
	if err == nil {
		t.Fatal("FastForwardToRemote overwrote untracked work")
	}
	if head != "" || present || advanced {
		t.Fatalf("failure state = head %q present %t advanced %t; want a bare refusal", head, present, advanced)
	}
	if head := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "stream/fixture")); head != localHead {
		t.Fatalf("local head moved from %s to %s despite the refusal", localHead, head)
	}
	if contents, err := os.ReadFile(filepath.Join(fixture.local, "remote.txt")); err != nil || string(contents) != "mine\n" {
		t.Fatalf("untracked work = %q (err %v); want it untouched", contents, err)
	}
}

func TestExecGitFastForwardToRemoteRefusesACheckoutItCannotMake(t *testing.T) {
	t.Parallel()
	fixture := newRemoteStreamFixture(t)
	// Give the stream branch a file main does not carry, publish it, then let
	// the remote move on so the local stream branch is strictly behind it.
	commitFile(t, fixture.local, "extra.txt", "extra\n", "feat: extra")
	runGit(t, fixture.local, "push", "origin", "stream/fixture")
	runGit(t, fixture.other, "fetch", "origin")
	runGit(t, fixture.other, "checkout", "stream/fixture")
	commitFile(t, fixture.other, "remote.txt", "remote\n", "feat: remote advance")
	runGit(t, fixture.other, "push", "origin", "stream/fixture")

	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()
	if err := git.Fetch(ctx, fixture.local); err != nil {
		t.Fatal(err)
	}
	// Stand on main and place an untracked file the checkout would overwrite;
	// the branch cannot be made current, so no fast-forward may be attempted.
	runGit(t, fixture.local, "checkout", "main")
	if err := os.WriteFile(filepath.Join(fixture.local, "extra.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := git.FastForwardToRemote(ctx, fixture.local, "stream/fixture", "origin/stream/fixture")
	if err == nil {
		t.Fatal("FastForwardToRemote reported success although the branch could not be checked out")
	}
	if !strings.Contains(err.Error(), "stream/fixture") {
		t.Fatalf("error = %v, want it to name the branch it could not check out", err)
	}
}

func TestExecGitCommitsAheadReportsAnUnreadableRange(t *testing.T) {
	t.Parallel()
	fixture := newRemoteStreamFixture(t)
	git := ExecGit{Timeout: time.Minute}
	// The upstream resolves, but the branch does not: the range cannot be
	// counted and the port must report the failure rather than zero.
	if _, err := git.CommitsAhead(context.Background(), fixture.local, "no-such-branch", "origin/stream/fixture"); err == nil {
		t.Fatal("CommitsAhead reported success for an unreadable commit range")
	}
}

func TestExecGitRestoreToReportsAnUnknownRevision(t *testing.T) {
	t.Parallel()
	root := stCovScratchRepo(t)
	git := ExecGit{Timeout: time.Minute}
	if err := git.RestoreTo(context.Background(), root, "no-such-revision"); err == nil {
		t.Fatal("RestoreTo accepted an unknown revision")
	}
	// A failed restore must not silently discard the worktree either.
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("still here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git.RestoreTo(context.Background(), root, "no-such-revision"); err == nil {
		t.Fatal("RestoreTo accepted an unknown revision on a dirty tree")
	}
	if contents, err := os.ReadFile(filepath.Join(root, "base.txt")); err != nil || string(contents) != "still here\n" {
		t.Fatalf("a refused RestoreTo changed the worktree: %q (err %v)", contents, err)
	}
}

func TestExecGitPushWithLeaseReportsAnUnknownLocalBranch(t *testing.T) {
	t.Parallel()
	fixture := newRemoteStreamFixture(t)
	git := ExecGit{Timeout: time.Minute}
	if _, err := git.PushWithLease(context.Background(), fixture.local, "no-such-branch", ""); err == nil {
		t.Fatal("PushWithLease published a branch that does not exist")
	}
}

func TestExecGitIsCleanFailsClosedOutsideARepository(t *testing.T) {
	t.Parallel()
	stCovRequireGit(t)
	git := ExecGit{Timeout: time.Minute}
	if _, err := git.IsClean(context.Background(), t.TempDir()); err == nil {
		t.Fatal("IsClean reported a directory that is not a repository as clean")
	}
}

func TestExecBumperApplyReportsAGoModuleScanFailure(t *testing.T) {
	t.Parallel()
	bumper := ExecBumper{Timeout: time.Minute}
	absent := filepath.Join(t.TempDir(), "absent")
	err := bumper.Apply(context.Background(), absent, Library{Name: "acme.test/library", Ecosystem: "go"})
	if err == nil {
		t.Fatal("Apply scanned a directory that does not exist without error")
	}
}

func TestExecGitCommitAllReportsAStagingOrCommitFailure(t *testing.T) {
	t.Parallel()
	root := stCovScratchRepo(t)
	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()

	t.Run("staging failure", func(t *testing.T) {
		blocker := filepath.Join(root, "blocker")
		if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_INDEX_FILE", filepath.Join(blocker, "index"))
		if err := os.WriteFile(filepath.Join(root, "work.txt"), []byte("work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := git.CommitAll(ctx, root, "feat: unstaged"); err == nil {
			t.Fatal("CommitAll reported success although staging failed")
		}
	})

	t.Run("commit refusal", func(t *testing.T) {
		// Git refuses a commit with an empty committer identity, which is the
		// one commit failure a fixture can produce without corrupting the
		// repository: the change is staged but nothing is committed.
		t.Setenv("GIT_AUTHOR_NAME", "")
		t.Setenv("GIT_COMMITTER_NAME", "")
		if err := os.WriteFile(filepath.Join(root, "work.txt"), []byte("work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := stCovHead(t, root)
		if _, ok, err := git.CommitAll(ctx, root, "feat: must be refused"); err == nil || ok {
			t.Fatalf("CommitAll = ok %t, err %v; want the commit refusal", ok, err)
		}
		if after := stCovHead(t, root); after != before {
			t.Fatalf("HEAD moved from %s to %s despite the refused commit", before, after)
		}
	})
}

func TestSyncRunBoundedDefaultsAnUnsetTimeout(t *testing.T) {
	t.Parallel()
	// A zero timeout means "use the default bound"; the command still runs.
	if _, err := runBounded(context.Background(), 0, t.TempDir(), nil, "go", "version"); err != nil {
		t.Fatalf("runBounded with a zero timeout: %v", err)
	}
}

func stCovEqualLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// A push destination that is not the fetch destination can accept the branch
// while the fetch URL cannot yet read it back; that re-read failure is
// reported rather than silently claiming the push landed.
func TestPushWithLeaseReportsAFailedRereadAfterThePush(t *testing.T) {
	t.Parallel()
	stCovRequireGit(t)
	base := t.TempDir()
	fetchRemote := filepath.Join(base, "fetch.git")
	pushRemote := filepath.Join(base, "push.git")
	for _, remote := range []string{fetchRemote, pushRemote} {
		runGit(t, "", "init", "--bare", "--initial-branch=main", remote)
	}
	work := filepath.Join(base, "work")
	runGit(t, "", "clone", fetchRemote, work)
	commitFile(t, work, "base.txt", "base\n", "feat: base")
	runGit(t, work, "push", "-u", "origin", "main")
	runGit(t, work, "checkout", "-b", "stream/reread")
	runGit(t, work, "remote", "set-url", "--push", "origin", pushRemote)

	git := ExecGit{Timeout: time.Minute}
	_, err := git.PushWithLease(context.Background(), work, "stream/reread", "")
	if err == nil || !strings.Contains(err.Error(), "re-read origin/stream/reread after pushing") {
		t.Fatalf("error = %v, want the failed re-read reported", err)
	}
	if remote := strings.TrimSpace(runGit(t, work, "ls-remote", "--heads", pushRemote, "refs/heads/stream/reread")); remote == "" {
		t.Fatal("the push itself did not land on the push destination")
	}
}

// A post-receive hook that rewrites the pushed ref makes the push exit 0 while
// origin holds a different commit; the verification must catch that.
func TestPushWithLeaseReportsAnOriginThatDisagreesAfterThePush(t *testing.T) {
	t.Parallel()
	stCovRequireGit(t)
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	base := t.TempDir()
	remote := filepath.Join(base, "origin.git")
	runGit(t, "", "init", "--bare", "--initial-branch=main", remote)
	work := filepath.Join(base, "work")
	runGit(t, "", "clone", remote, work)
	commitFile(t, work, "base.txt", "base\n", "feat: base")
	runGit(t, work, "push", "-u", "origin", "main")
	runGit(t, work, "checkout", "-b", "stream/hooked")
	runGit(t, work, "checkout", "main")
	commitFile(t, work, "later.txt", "later\n", "feat: later")
	runGit(t, work, "push", "origin", "main")
	runGit(t, work, "checkout", "stream/hooked")
	mainSHA := strings.TrimSpace(runGit(t, work, "rev-parse", "main"))

	hook := filepath.Join(remote, "hooks", "post-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ngit update-ref refs/heads/stream/hooked refs/heads/main\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	git := ExecGit{Timeout: time.Minute}
	_, err := git.PushWithLease(context.Background(), work, "stream/hooked", "")
	if err == nil || !strings.Contains(err.Error(), "did not land the intended commit") {
		t.Fatalf("error = %v, want the diverged origin reported", err)
	}
	remoteRef := strings.TrimSpace(runGit(t, work, "ls-remote", "--heads", remote, "refs/heads/stream/hooked"))
	if fields := strings.Fields(remoteRef); len(fields) != 2 || fields[0] != mainSHA {
		t.Fatalf("origin/stream/hooked = %q, want the hook's %s", remoteRef, mainSHA)
	}
}
