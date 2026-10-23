package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Pull retries a transport failure and returns success once a later attempt
// gets through, so one dropped SSH handshake does not fail a whole sync.
// pull's sleep is a recorder, not a package-level mutable var (the seam is a
// function parameter — see gitops.go's pull), so retry-exhaustion paths run
// at full speed while still proving exactly how many times, and for how
// long, Pull waited between attempts.
func TestLgCovPullRetriesTransientFailureThenSucceeds(t *testing.T) {
	repoPath := t.TempDir()
	state := filepath.Join(repoPath, "attempts")
	t.Setenv("LGCOV_PULL_STATE", state)

	lgCovFakeGit(t, `
case "$1" in
  pull)
    n=0
    if [ -f "$LGCOV_PULL_STATE" ]; then n=$(cat "$LGCOV_PULL_STATE"); fi
    n=$((n + 1))
    printf '%s' "$n" > "$LGCOV_PULL_STATE"
    if [ "$n" -lt 3 ]; then
      echo "fatal: Connection closed by 203.0.113.7 port 22" >&2
      exit 1
    fi
    echo "Already up to date."
    exit 0;;
esac
exit 1
`)

	var slept []time.Duration
	if err := pull(repoPath, func(d time.Duration) { slept = append(slept, d) }); err != nil {
		t.Fatalf("pull: %v", err)
	}
	raw, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != "3" {
		t.Fatalf("pull attempts = %s, want 3 (two transient failures then success)", got)
	}
	wantSlept := []time.Duration{pullRetryDelay(repoPath, 0), pullRetryDelay(repoPath, 1)}
	if len(slept) != len(wantSlept) || slept[0] != wantSlept[0] || slept[1] != wantSlept[1] {
		t.Fatalf("pull slept %v, want %v (one backoff before each of the two retries)", slept, wantSlept)
	}
}

// Once the five attempts are exhausted the last transport error is returned, so
// the caller can report the real failure instead of a silent success.
func TestLgCovPullReturnsLastErrorAfterExhaustingRetries(t *testing.T) {
	repoPath := t.TempDir()
	state := filepath.Join(repoPath, "attempts")
	t.Setenv("LGCOV_PULL_STATE", state)

	lgCovFakeGit(t, `
case "$1" in
  pull)
    n=0
    if [ -f "$LGCOV_PULL_STATE" ]; then n=$(cat "$LGCOV_PULL_STATE"); fi
    n=$((n + 1))
    printf '%s' "$n" > "$LGCOV_PULL_STATE"
    echo "fatal: Connection timed out" >&2
    exit 1;;
esac
exit 1
`)

	var slept []time.Duration
	err := pull(repoPath, func(d time.Duration) { slept = append(slept, d) })
	if err == nil {
		t.Fatal("pull should return an error after every attempt failed")
	}
	if !strings.Contains(err.Error(), "Connection timed out") {
		t.Fatalf("error = %q, want the last transport failure", err.Error())
	}
	raw, readErr := os.ReadFile(state)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.TrimSpace(string(raw)); got != "5" {
		t.Fatalf("pull attempts = %s, want 5", got)
	}
	if len(slept) != 4 {
		t.Fatalf("pull slept %d times, want 4 (once before each retry, never after the fifth and final attempt)", len(slept))
	}
	for attempt, d := range slept {
		if want := pullRetryDelay(repoPath, attempt); d != want {
			t.Fatalf("pull slept %s before attempt %d, want %s", d, attempt+1, want)
		}
	}
}

// A refusal that requires a human decision (a non-fast-forward) is returned on
// the first attempt; retrying it would only delay the report.
func TestLgCovPullReturnsNonTransientFailureWithoutRetrying(t *testing.T) {
	logPath := lgCovFakeGit(t, `
case "$1" in
  pull) echo "fatal: Not possible to fast-forward, aborting." >&2; exit 1;;
esac
exit 1
`)

	err := Pull(t.TempDir())
	if err == nil {
		t.Fatal("Pull should return the fast-forward refusal")
	}
	if !strings.Contains(err.Error(), "Not possible to fast-forward") {
		t.Fatalf("error = %q, want the refusal text", err.Error())
	}
	if got := len(lgCovInvocations(t, logPath)); got != 1 {
		t.Fatalf("pull invocations = %d, want 1 (no retry for a refusal)", got)
	}
}

// The unpushed-work probe is built from several git calls; each failure must be
// surfaced rather than silently reported as "no unpushed work".
func TestLgCovUnpushedWorkSurfacesGitFailures(t *testing.T) {
	headRef := "refs/heads/main\t0123456789012345678901234567890123456789\t\t"

	t.Run("remote ref probe fails", func(t *testing.T) {
		lgCovFakeGit(t, "exit 1")
		if _, _, err := UnpushedWork(t.TempDir()); err == nil {
			t.Fatal("want an error when listing refs/remotes fails")
		}
	})

	t.Run("branch listing fails", func(t *testing.T) {
		lgCovFakeGit(t, `
case "$1" in
  for-each-ref) exit 1;;
esac
exit 0
`)
		if _, _, err := UnpushedWork(t.TempDir()); err == nil {
			t.Fatal("want an error when listing local branches fails")
		}
	})

	t.Run("commit listing fails", func(t *testing.T) {
		lgCovFakeGit(t, `
case "$1" in
  for-each-ref) printf '%s\n' "`+headRef+`"; exit 0;;
  log) exit 1;;
esac
exit 0
`)
		if _, _, err := UnpushedWork(t.TempDir()); err == nil {
			t.Fatal("want an error when the unpushed log fails")
		}
	})

	t.Run("malformed commit record", func(t *testing.T) {
		lgCovFakeGit(t, `
case "$1" in
  for-each-ref) printf '%s\n' "`+headRef+`"; exit 0;;
  log) echo "not-a-valid-record"; exit 0;;
esac
exit 0
`)
		if _, _, err := UnpushedWork(t.TempDir()); err == nil {
			t.Fatal("want a parse error for a git log record without tabs")
		}
	})

	t.Run("worktree listing fails", func(t *testing.T) {
		lgCovFakeGit(t, `
case "$1" in
  for-each-ref) printf '%s\n' "`+headRef+`"; exit 0;;
  log) printf 'aaaa\tbbbb subject\t\n'; exit 0;;
  worktree) exit 1;;
esac
exit 0
`)
		if _, _, err := UnpushedWork(t.TempDir()); err == nil {
			t.Fatal("want an error when listing worktrees fails")
		}
	})
}

// A for-each-ref line without the four tab-separated fields is skipped instead
// of producing a branch with an empty ref.
func TestLgCovUnpushableBranchesSkipsShortRecords(t *testing.T) {
	lgCovFakeGit(t, `
case "$1" in
  for-each-ref) echo "refs/heads/only"; exit 0;;
esac
exit 0
`)

	branches, err := unpushableBranches(t.TempDir())
	if err != nil {
		t.Fatalf("unpushableBranches: %v", err)
	}
	if len(branches) != 0 {
		t.Fatalf("branches = %+v, want none from a record with too few fields", branches)
	}
}

func TestLgCovUnpushableBranchesErrorsOutsideRepository(t *testing.T) {
	t.Parallel()
	if _, err := unpushableBranches(t.TempDir()); err == nil {
		t.Fatal("unpushableBranches outside a git repository should error")
	}
}

// Tracking's rev-list accounting turns a failed call and any unexpected output
// into errors rather than a silently wrong ahead/behind count.
func TestLgCovTrackingSurfacesRevListFailures(t *testing.T) {
	lgCovTrackingGit := func(t *testing.T, revListBody string) string {
		t.Helper()
		return lgCovFakeGit(t, `
case "$1" in
  symbolic-ref) echo main; exit 0;;
  config) echo refs/heads/main; exit 0;;
  rev-parse) echo origin/main; exit 0;;
  rev-list) `+revListBody+`;;
esac
exit 0
`)
	}

	t.Run("rev-list fails", func(t *testing.T) {
		lgCovTrackingGit(t, "exit 1")
		got, err := Tracking(t.TempDir())
		if err != nil {
			t.Fatalf("Tracking should report the state it knows, not an error: %v", err)
		}
		if got.Branch != "main" || got.Upstream != "origin/main" {
			t.Fatalf("Tracking = %+v, want branch main tracking origin/main", got)
		}
		if got.Ahead != 0 || got.Behind != 0 {
			t.Fatalf("Ahead/Behind = %d/%d, want 0/0 when the counts could not be read", got.Ahead, got.Behind)
		}
	})

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "three fields", body: `echo "1 2 3"; exit 0`},
		{name: "non-numeric ahead", body: `echo "x 2"; exit 0`},
		{name: "non-numeric behind", body: `echo "1 y"; exit 0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lgCovTrackingGit(t, tc.body)
			if _, err := Tracking(t.TempDir()); err == nil {
				t.Fatalf("Tracking with rev-list output %q should error", tc.body)
			}
		})
	}
}

// Status composes git status, git stash list, and the unpushed probe; a failure
// in any of them must be returned, not flattened into an empty-looking status.
func TestLgCovStatusSurfacesGitFailures(t *testing.T) {
	t.Run("status fails", func(t *testing.T) {
		lgCovGitIdentity(t)
		if _, err := Status(t.TempDir()); err == nil {
			t.Fatal("Status outside a git repository should error")
		}
	})

	t.Run("stash listing fails", func(t *testing.T) {
		lgCovFakeGit(t, `
case "$1" in
  status) exit 0;;
  stash) exit 1;;
esac
exit 0
`)
		if _, err := Status(t.TempDir()); err == nil {
			t.Fatal("want an error when git stash list fails")
		}
	})

	t.Run("unpushed probing fails", func(t *testing.T) {
		lgCovFakeGit(t, `
case "$1" in
  status) exit 0;;
  stash) exit 0;;
  for-each-ref) exit 1;;
esac
exit 0
`)
		if _, err := Status(t.TempDir()); err == nil {
			t.Fatal("want an error when the unpushed probe fails")
		}
	})
}

// AddCommit returns an error when it cannot tell whether anything was staged,
// and when the commit itself fails, instead of reporting an idempotent no-op.
func TestLgCovAddCommitSurfacesGitFailures(t *testing.T) {
	t.Run("staged diff fails", func(t *testing.T) {
		lgCovFakeGit(t, `
case "$1" in
  add) exit 0;;
  diff) exit 128;;
esac
exit 0
`)
		committed, err := AddCommit(t.TempDir(), "msg", "file.txt")
		if err == nil {
			t.Fatal("want an error when git diff --cached --quiet fails unexpectedly")
		}
		if committed {
			t.Fatal("committed = true on error, want false")
		}
		if !strings.Contains(err.Error(), "inspect staged changes") {
			t.Fatalf("error = %q, want it to name the staged-change inspection", err.Error())
		}
	})

	t.Run("commit fails", func(t *testing.T) {
		lgCovFakeGit(t, `
case "$1" in
  add) exit 0;;
  diff) exit 1;;
  commit) exit 1;;
esac
exit 0
`)
		committed, err := AddCommit(t.TempDir(), "msg", "file.txt")
		if err == nil {
			t.Fatal("want an error when git commit fails")
		}
		if committed {
			t.Fatal("committed = true after a failed commit, want false")
		}
	})
}

// LocalState reaches its second git call only when the working tree is clean;
// a failure there must be reported, not read as "no unpushed commits".
func TestLgCovLocalStateSurfacesLogFailure(t *testing.T) {
	lgCovFakeGit(t, `
case "$1" in
  status) exit 0;;
  log) exit 1;;
esac
exit 0
`)

	dirty, reason, err := LocalState(t.TempDir())
	if err == nil {
		t.Fatal("want an error when the unpushed-commit log fails")
	}
	if dirty || reason != "" {
		t.Fatalf("LocalState = (%v, %q) on error, want (false, \"\")", dirty, reason)
	}
}

func TestLgCovLocalStateErrorsOutsideRepository(t *testing.T) {
	t.Parallel()
	if _, _, err := LocalState(t.TempDir()); err == nil {
		t.Fatal("LocalState outside a git repository should error")
	}
}

// A stat failure that is not simple absence (here ENOTDIR, from a path under a
// regular file) is a real error RebaseInProgress must return.
func TestLgCovRebaseInProgressSurfacesStatFailure(t *testing.T) {
	blocker := lgCovWriteFile(t, t.TempDir(), "blocker", "not a directory\n")
	t.Setenv("LGCOV_GITPATH", filepath.Join(blocker, "rebase-merge"))

	lgCovFakeGit(t, `
case "$1" in
  rev-parse) echo "$LGCOV_GITPATH"; exit 0;;
esac
exit 1
`)

	inProgress, err := RebaseInProgress(t.TempDir())
	if err == nil {
		t.Fatal("want the stat failure surfaced, not read as no rebase in progress")
	}
	if inProgress {
		t.Fatal("inProgress = true on a stat error, want false")
	}
}

// DefaultBranch falls back to `git remote show origin` when origin/HEAD cannot
// be resolved, and must still find the branch name.
func TestLgCovDefaultBranchFallsBackToRemoteShow(t *testing.T) {
	lgCovFakeGit(t, `
case "$1" in
  symbolic-ref) exit 1;;
  remote)
    case "$2" in
      set-head) exit 1;;
      show) printf '  * remote origin\n    HEAD branch: trunk\n'; exit 0;;
    esac;;
esac
exit 1
`)

	got, err := DefaultBranch(t.TempDir())
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "trunk" {
		t.Fatalf("DefaultBranch = %q, want trunk", got)
	}
}

func TestLgCovDefaultBranchErrorsWhenRemoteShowFails(t *testing.T) {
	lgCovFakeGit(t, "exit 1")

	if _, err := DefaultBranch(t.TempDir()); err == nil {
		t.Fatal("DefaultBranch should error when neither origin/HEAD nor git remote show resolves")
	}
}

func TestLgCovDefaultBranchErrorsWhenRemoteShowHasNoHEADBranch(t *testing.T) {
	lgCovFakeGit(t, `
case "$1" in
  symbolic-ref) exit 1;;
  remote)
    case "$2" in
      set-head) exit 1;;
      show) printf '  * remote origin\n    Fetch URL: /tmp/origin\n'; exit 0;;
    esac;;
esac
exit 1
`)

	repoPath := t.TempDir()
	_, err := DefaultBranch(repoPath)
	if err == nil {
		t.Fatal("DefaultBranch should error when git remote show names no HEAD branch")
	}
	if !strings.Contains(err.Error(), repoPath) {
		t.Fatalf("error = %q, want it to name %s", err.Error(), repoPath)
	}
}
