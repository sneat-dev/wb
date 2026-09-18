package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/console"
)

// TestWTCoreCovFreshnessClassifiesEveryOutcome drives the freshness classifier
// through every documented outcome with an injected Git runner, so each branch
// (fetch failure, missing target, drift, ahead/behind classification) is
// asserted on the returned receipt rather than merely executed.
func TestWTCoreCovFreshnessClassifiesEveryOutcome(t *testing.T) {
	const target = "main"

	type wtCoreCovFreshnessStub struct {
		localOut   string
		localErr   error
		fetchErr   error
		probeOut   string
		probeErr   error
		remoteOut  string
		remoteErr  error
		countsOut  string
		countsErr  error
		advertise  string
		advertiseE error
	}
	run := func(s wtCoreCovFreshnessStub) canonicalFreshnessGit {
		return func(_ context.Context, _ string, args ...string) (string, error) {
			switch args[0] {
			case "rev-parse":
				if len(args) > 1 && args[1] == "HEAD" {
					return s.localOut, s.localErr
				}
				return s.remoteOut, s.remoteErr
			case "fetch":
				return "", s.fetchErr
			case "ls-remote":
				if s.fetchErr != nil {
					return s.probeOut, s.probeErr
				}
				return s.advertise, s.advertiseE
			case "rev-list":
				return s.countsOut, s.countsErr
			}
			return "", fmt.Errorf("unexpected git command %v", args)
		}
	}

	cases := []struct {
		name       string
		stub       wtCoreCovFreshnessStub
		wantStatus string
		wantErrHas string
		wantAhead  int
		wantBehind int
		wantFetch  bool
		wantDrift  bool
	}{
		{
			name:       "unreadable local head",
			stub:       wtCoreCovFreshnessStub{localErr: errors.New("boom")},
			wantStatus: CanonicalFreshnessFetchError,
			wantErrHas: "boom",
		},
		{
			name:       "offline remote",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", fetchErr: errors.New("no network"), probeErr: errors.New("unreachable")},
			wantStatus: CanonicalFreshnessOffline,
			wantErrHas: "remote probe failed",
		},
		{
			name:       "remote advertises no target",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", fetchErr: errors.New("nope"), probeOut: "\n"},
			wantStatus: CanonicalFreshnessMissing,
			wantErrHas: "not advertised",
		},
		{
			name:       "fetch failed while target exists",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", fetchErr: errors.New("nope"), probeOut: "bbbb\trefs/heads/main\n"},
			wantStatus: CanonicalFreshnessFetchError,
			wantErrHas: "fetch origin/main failed",
		},
		{
			name:       "fetched target unavailable locally",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteErr: errors.New("unknown revision")},
			wantFetch:  true,
			wantStatus: CanonicalFreshnessMissing,
			wantErrHas: "unavailable locally",
		},
		{
			name:       "comparison failed",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteOut: "bbbb\n", countsErr: errors.New("nope")},
			wantFetch:  true,
			wantStatus: CanonicalFreshnessFetchError,
			wantErrHas: "compare HEAD to",
		},
		{
			name:       "comparison shape unexpected",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteOut: "bbbb\n", countsOut: "1\n"},
			wantFetch:  true,
			wantStatus: CanonicalFreshnessFetchError,
			wantErrHas: "returned",
		},
		{
			name:       "ahead count unparseable",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteOut: "bbbb\n", countsOut: "x 0\n"},
			wantFetch:  true,
			wantStatus: CanonicalFreshnessFetchError,
			wantErrHas: "parse ahead count",
		},
		{
			name:       "behind count unparseable",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteOut: "bbbb\n", countsOut: "0 y\n"},
			wantFetch:  true,
			wantStatus: CanonicalFreshnessFetchError,
			wantErrHas: "parse behind count",
		},
		{
			name:       "post-fetch verification failed",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteOut: "bbbb\n", countsOut: "0 0\n", advertiseE: errors.New("gone")},
			wantFetch:  true,
			wantStatus: CanonicalFreshnessFetchError,
			wantErrHas: "verify origin/main after fetch failed",
		},
		{
			name:       "target disappeared after fetch",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteOut: "bbbb\n", countsOut: "0 0\n"},
			wantFetch:  true,
			wantStatus: CanonicalFreshnessDrifted,
			wantErrHas: "disappeared after fetch",
			wantDrift:  true,
		},
		{
			name:       "target moved after fetch",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteOut: "bbbb\n", countsOut: "0 0\n", advertise: "cccc\trefs/heads/main\n"},
			wantFetch:  true,
			wantStatus: CanonicalFreshnessDrifted,
			wantErrHas: "moved from bbbb to cccc",
			wantDrift:  true,
		},
		{
			name:       "current",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteOut: "bbbb\n", countsOut: "0 0\n", advertise: "bbbb\trefs/heads/main\n"},
			wantStatus: CanonicalFreshnessCurrent,
			wantFetch:  true,
		},
		{
			name:       "ahead",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteOut: "bbbb\n", countsOut: "2 0\n", advertise: "bbbb\trefs/heads/main\n"},
			wantStatus: CanonicalFreshnessAhead,
			wantAhead:  2,
			wantFetch:  true,
		},
		{
			name:       "stale",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteOut: "bbbb\n", countsOut: "0 3\n", advertise: "bbbb\trefs/heads/main\n"},
			wantStatus: CanonicalFreshnessStale,
			wantBehind: 3,
			wantFetch:  true,
		},
		{
			name:       "diverged",
			stub:       wtCoreCovFreshnessStub{localOut: "aaaa\n", remoteOut: "bbbb\n", countsOut: "1 1\n", advertise: "bbbb\trefs/heads/main\n"},
			wantStatus: CanonicalFreshnessDiverged,
			wantAhead:  1,
			wantBehind: 1,
			wantFetch:  true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := inspectCanonicalFreshnessWith(context.Background(), "/canonical", target, run(testCase.stub))
			if result.Target != target || result.RemoteRef != "origin/"+target {
				t.Fatalf("receipt identity = %#v", result)
			}
			if result.Status != testCase.wantStatus {
				t.Fatalf("status = %q, want %q (error %q)", result.Status, testCase.wantStatus, result.Error)
			}
			if result.Ahead != testCase.wantAhead || result.Behind != testCase.wantBehind {
				t.Fatalf("counts = %d/%d, want %d/%d", result.Ahead, result.Behind, testCase.wantAhead, testCase.wantBehind)
			}
			if result.Fetched != testCase.wantFetch {
				t.Fatalf("fetched = %v, want %v", result.Fetched, testCase.wantFetch)
			}
			if result.TargetDrift != testCase.wantDrift {
				t.Fatalf("target drift = %v, want %v", result.TargetDrift, testCase.wantDrift)
			}
			if testCase.wantErrHas != "" && !strings.Contains(result.Error, testCase.wantErrHas) {
				t.Fatalf("error %q does not mention %q", result.Error, testCase.wantErrHas)
			}
			if testCase.wantErrHas == "" && result.Error != "" {
				t.Fatalf("unexpected error %q", result.Error)
			}
			if result.ObservedAt.IsZero() {
				t.Fatal("observed_at must always be recorded")
			}
		})
	}
}

// TestWTCoreCovDescribeExitErrorKeepsStderr asserts the two documented
// behaviours: a real exit error gains the child's own stderr text, and an
// ordinary error is returned unchanged.
func TestWTCoreCovDescribeExitErrorKeepsStderrAndPlainErrors(t *testing.T) {
	command := exec.Command("git", "-C", filepath.Join(t.TempDir(), "absent"), "rev-parse", "HEAD")
	command.Env = console.Env()
	_, err := command.Output()
	if err == nil {
		t.Fatal("git in an absent directory unexpectedly succeeded")
	}
	described := describeExitError(err)
	if described.Error() == err.Error() {
		t.Fatalf("exit error was not enriched with stderr: %q", described)
	}
	if !strings.Contains(described.Error(), "fatal") {
		t.Fatalf("described error %q does not carry Git's own diagnostic", described)
	}

	plain := errors.New("plain failure")
	if got := describeExitError(plain); got != plain {
		t.Fatalf("plain error = %v, want identity %v", got, plain)
	}
}

// TestWTCoreCovPatchIDHelpers proves the patch-id comparison on real history:
// a replayed patch is recognised across rewritten commits, an unrelated patch
// is not, and an empty sealed range proves nothing.
func TestWTCoreCovPatchIDHelpers(t *testing.T) {
	ctx := context.Background()
	repository := newJournalWorktree(t)

	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "base.txt")
	gitTest(t, repository, "commit", "-m", "base")
	base := strings.TrimSpace(gitTestOutput(t, repository, "rev-parse", "HEAD"))

	// sealed: one commit adding replayed.txt.
	gitTest(t, repository, "checkout", "-b", "sealed")
	wtCoreCovCommitFile(t, repository, "replayed.txt", "replayed\n", "sealed work")
	sealedHead := strings.TrimSpace(gitTestOutput(t, repository, "rev-parse", "HEAD"))

	// current: the same patch replayed as a brand-new commit with a new SHA.
	gitTest(t, repository, "checkout", base)
	gitTest(t, repository, "checkout", "-b", "current")
	wtCoreCovCommitFile(t, repository, "replayed.txt", "replayed\n", "replayed as new commit")
	currentHead := strings.TrimSpace(gitTestOutput(t, repository, "rev-parse", "HEAD"))
	if currentHead == sealedHead {
		t.Fatal("fixture failed to rewrite the commit")
	}

	ids, err := commitPatchIDs(ctx, repository, base+".."+sealedHead)
	if err != nil {
		t.Fatalf("commitPatchIDs sealed: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("sealed patch ids = %#v, want exactly one", ids)
	}
	for _, commits := range ids {
		if len(commits) != 1 || commits[0] != sealedHead {
			t.Fatalf("sealed patch maps to %v, want [%s]", commits, sealedHead)
		}
	}

	shared, err := commitsShareEveryPatchIDByRebase(ctx, repository, sealedHead, currentHead)
	if err != nil {
		t.Fatalf("share by rebase: %v", err)
	}
	if !shared {
		t.Fatal("a replayed patch must be recognised as shared by patch-id")
	}

	// An unrelated patch on current must not be reported as shared.
	gitTest(t, repository, "checkout", "-b", "unrelated", base)
	wtCoreCovCommitFile(t, repository, "other.txt", "other\n", "unrelated work")
	unrelatedHead := strings.TrimSpace(gitTestOutput(t, repository, "rev-parse", "HEAD"))
	shared, err = commitsShareEveryPatchIDByRebase(ctx, repository, sealedHead, unrelatedHead)
	if err != nil {
		t.Fatalf("share by rebase (unrelated): %v", err)
	}
	if shared {
		t.Fatal("unrelated patches must not be reported as shared")
	}

	// Nothing committed past the shared base proves nothing.
	shared, err = commitsShareEveryPatchIDByRebase(ctx, repository, base, base)
	if err != nil {
		t.Fatalf("share by rebase (empty): %v", err)
	}
	if shared {
		t.Fatal("an empty sealed range must not authorize cleanup")
	}

	if _, err := commitsShareEveryPatchIDByRebase(ctx, repository, "0000000000000000000000000000000000000000", currentHead); err == nil {
		t.Fatal("an unresolvable sealed head must be reported as an error")
	}

	if _, err := commitPatchIDs(ctx, repository, "no-such-revision"); err == nil {
		t.Fatal("an unresolvable revision range must be reported as an error")
	} else if !strings.Contains(err.Error(), "list patches") {
		t.Fatalf("error %q does not name the failing step", err)
	}
}

// TestWTCoreCovEnsureManifestFillsOnlyAMissingRecord asserts idempotence: the
// first call writes the manifest, the second leaves the immutable record alone.
func TestWTCoreCovEnsureManifestFillsOnlyAMissingRecord(t *testing.T) {
	worktree := newJournalWorktree(t)
	manifest := newCreatedManifest("wtcore-ensure")
	if err := EnsureManifest(worktree, manifest); err != nil {
		t.Fatalf("first EnsureManifest: %v", err)
	}
	first, err := ReadManifest(worktree)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if first.EffortID != manifest.EffortID {
		t.Fatalf("manifest = %#v", first)
	}
	if err := EnsureManifest(worktree, newCreatedManifest("wtcore-other")); err != nil {
		t.Fatalf("idempotent EnsureManifest: %v", err)
	}
	second, err := ReadManifest(worktree)
	if err != nil {
		t.Fatalf("reread manifest: %v", err)
	}
	if second.EffortID != manifest.EffortID {
		t.Fatalf("manifest was replaced: %#v", second)
	}
}

// TestWTCoreCovHeartbeatScopedToCurrentDirectory asserts the two halves of the
// heartbeat contract: touching a non-worktree is a silent no-op, and touching
// from inside a WB worktree records that worktree and nothing else.
func TestWTCoreCovHeartbeatScopedToCurrentDirectory(t *testing.T) {
	plain := t.TempDir()
	TouchHeartbeat(plain, "wb list")
	if _, err := os.Stat(filepath.Join(plain, journalRootDirectory, journalLocalDirectory, heartbeatName)); !os.IsNotExist(err) {
		t.Fatalf("heartbeat was written outside a WB worktree: %v", err)
	}
	if got := HeartbeatAt(plain); !got.IsZero() {
		t.Fatalf("HeartbeatAt on a non-worktree = %v, want zero", got)
	}

	worktree := newJournalWorktree(t)
	if err := EnsureManifest(worktree, newCreatedManifest("wtcore-heartbeat")); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	before := time.Now().UTC().Add(-time.Minute)
	t.Chdir(filepath.Join(worktree, ".wb", "local"))
	TouchHeartbeatForCurrentDirectory("wb log checkpoint")

	at := HeartbeatAt(worktree)
	if at.IsZero() {
		t.Fatal("TouchHeartbeatForCurrentDirectory did not record the containing worktree")
	}
	if at.Before(before) {
		t.Fatalf("heartbeat at %v predates the touch at %v", at, before)
	}
	if at.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("heartbeat at %v is implausibly in the future", at)
	}
}

// TestWTCoreCovHeartbeatAtRejectsCorruptRecord asserts a malformed heartbeat
// reads as "unknown" rather than as some earlier or fabricated time.
func TestWTCoreCovHeartbeatAtRejectsCorruptRecord(t *testing.T) {
	worktree := newJournalWorktree(t)
	if err := EnsureManifest(worktree, newCreatedManifest("wtcore-corrupt")); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	heartbeat := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, heartbeatName)
	if err := os.WriteFile(heartbeat, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := HeartbeatAt(worktree); !got.IsZero() {
		t.Fatalf("corrupt heartbeat = %v, want zero", got)
	}
	// A directory tree with no WB journal anywhere above it is not a worktree,
	// so the upward walk reaches the filesystem root and reports nothing.
	if root, err := worktreeRootOf(filepath.Join(t.TempDir(), "no-manifest")); err != nil || root != "" {
		t.Fatalf("worktreeRootOf outside a worktree = %q, err=%v", root, err)
	}
}

// TestWTCoreCovNewestChangedFileTimeReadsRenameAndDeletion asserts Git's own
// porcelain answer is used, including the rename target and a path that no
// longer exists on disk.
func TestWTCoreCovNewestChangedFileTimeReadsRenameAndDeletion(t *testing.T) {
	ctx := context.Background()
	repository := newJournalWorktree(t)
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "tracked.txt")
	gitTest(t, repository, "commit", "-m", "base")

	if got := NewestChangedFileTime(ctx, repository); !got.IsZero() {
		t.Fatalf("clean worktree reported activity at %v", got)
	}

	// A rename reports "old -> new"; the new path is the one that was written.
	gitTest(t, repository, "mv", "tracked.txt", "renamed.txt")
	renamed := filepath.Join(repository, "renamed.txt")
	info, err := os.Lstat(renamed)
	if err != nil {
		t.Fatal(err)
	}
	got := NewestChangedFileTime(ctx, repository)
	if got.IsZero() {
		t.Fatal("a renamed file must count as activity")
	}
	if !got.Equal(info.ModTime().UTC()) {
		t.Fatalf("activity = %v, want rename target mtime %v", got, info.ModTime().UTC())
	}

	// A tracked file deleted from disk cannot be stat'd and must be skipped
	// rather than abort the read.
	gitTest(t, repository, "commit", "-am", "rename")
	if err := os.Remove(renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = NewestChangedFileTime(ctx, repository)
	if got.IsZero() {
		t.Fatal("the untracked file that still exists must be reported")
	}
	freshInfo, err := os.Lstat(filepath.Join(repository, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(freshInfo.ModTime().UTC()) {
		t.Fatalf("activity = %v, want the existing file mtime %v", got, freshInfo.ModTime().UTC())
	}

	if _, err := gitRawOutput(ctx, filepath.Join(t.TempDir(), "not-a-repository"), "status", "--porcelain"); err == nil {
		t.Fatal("gitRawOutput outside a repository must fail")
	}
}

// TestWTCoreCovNewestWorkLogEventTimeReadsRealJournalEntries asserts the Work
// Log freshness signal is scoped to a real WB journal directory.
//
// The positive case is currently unreachable: newestWorkLogEventTime opens the
// journal with os.NewFile(fd, "wb-journal") and then calls DirEntry.Info(),
// which resolves each entry name relative to that synthetic file name
// ("wb-journal/manifest.yaml"), so the lstat always fails and the function
// always returns the zero time. Covering it would require a source change, so
// only the honest negative behaviour is asserted here; see the report.
func TestWTCoreCovNewestWorkLogEventTimeReadsRealJournalEntries(t *testing.T) {
	if got := newestWorkLogEventTime(t.TempDir()); !got.IsZero() {
		t.Fatalf("non-worktree work log signal = %v, want zero", got)
	}
}

// TestWTCoreCovExtraStringTrimsOnlyStrings asserts the helper reports an empty
// string for a missing key and for a non-string value, and trims a real one.
func TestWTCoreCovExtraStringTrimsOnlyStrings(t *testing.T) {
	extra := map[string]any{"present": "  value  ", "number": 7, "null": nil}
	if got := extraString(extra, "present"); got != "value" {
		t.Fatalf("extraString(present) = %q, want %q", got, "value")
	}
	for _, key := range []string{"missing", "number", "null"} {
		if got := extraString(extra, key); got != "" {
			t.Fatalf("extraString(%s) = %q, want empty", key, got)
		}
	}
}

// wtCoreCovCommitFile writes and commits one file in repository.
func wtCoreCovCommitFile(t *testing.T, repository, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repository, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", name)
	gitTest(t, repository, "commit", "-m", message)
}
