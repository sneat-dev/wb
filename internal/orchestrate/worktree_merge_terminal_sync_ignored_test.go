package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

const ignoredSyncRef = "refs/remotes/origin/main"

func expectSyncGit(f *runnertest.Fake, stdout string, err error, args ...string) {
	f.Expect(func(c runnertest.Call) bool {
		return c.Name == "git" && reflect.DeepEqual(c.Args, args)
	}, runner.Result{Stdout: stdout, ExitCode: exitCodeForSyncErr(err)}, err)
}

func exitCodeForSyncErr(err error) int {
	if err != nil {
		return 128
	}
	return 0
}

func expectCheckIgnore(f *runnertest.Fake, result runner.Result, err error, observe func(runnertest.Call)) {
	f.Expect(func(c runnertest.Call) bool {
		if c.Name != "git" || len(c.Args) < 3 || c.Args[2] != "check-ignore" {
			return false
		}
		if observe != nil {
			observe(c)
		}
		return true
	}, result, err)
}

// expectIgnoredSyncPrelude scripts the observations that precede the
// ignore-rule evaluation for one untracked path set.
func expectIgnoredSyncPrelude(f *runnertest.Fake, untracked, gitignore string) {
	expectSyncGit(f, untracked, nil, "ls-files", "--others", "--exclude-standard", "-z")
	expectSyncGit(f, "", nil, "diff", "--name-only", "--no-renames", "-z", "HEAD", ignoredSyncRef)
	expectSyncGit(f, "/private/canonical/.git\n", nil, "rev-parse", "--absolute-git-dir")
	expectSyncGit(f, "100644 blob abc\t.gitignore\x00", nil, "--literal-pathspecs", "ls-tree", "-z", ignoredSyncRef, "--", ".gitignore")
	expectSyncGit(f, gitignore, nil, "cat-file", "blob", "abc")
}

func expectCanonicalSyncHead(f *runnertest.Fake, branch, status string) {
	expectSyncGit(f, branch+"\n", nil, "branch", "--show-current")
	expectSyncGit(f, status, nil, "status", "--porcelain=v1")
}

func TestCanonicalSyncFastForwardsOverUntrackedPathIgnoredByLandedTarget(t *testing.T) {
	t.Parallel()
	f := runnertest.New(t)
	expectCanonicalSyncHead(f, "main", "?? profile.cov\n")
	expectSyncGit(f, "before\n", nil, "rev-parse", "--verify", "HEAD^{commit}")
	expectSyncGit(f, "", nil, "fetch", "--no-tags", "origin", "+refs/heads/main:"+ignoredSyncRef)
	expectIgnoredSyncPrelude(f, "profile.cov\x00", "**/profile.cov\n")
	var scratch, materialized string
	var stdin []byte
	expectCheckIgnore(f, runner.Result{Stdout: ".gitignore\x001\x00**/profile.cov\x00./profile.cov\x00"}, nil, func(c runnertest.Call) {
		scratch = c.Dir
		stdin = c.Input
		content, _ := os.ReadFile(filepath.Join(c.Dir, ".gitignore"))
		materialized = string(content)
	})
	expectSyncGit(f, "", nil, "merge", "--ff-only", ignoredSyncRef)
	expectSyncGit(f, "after\n", nil, "rev-parse", "--verify", "HEAD^{commit}")
	expectSyncGit(f, "landing\n", nil, "merge-base", "landing", "after")
	expectSyncGit(f, "", nil, "status", "--porcelain=v1")
	var updates []CheckoutUpdate
	status, err := syncCanonicalMergeTargetWithRunner(t.Context(), f, "/private/canonical", "main", "landing", 0, 0, func(_ context.Context, u CheckoutUpdate) { updates = append(updates, u) })
	if err != nil || status != "fast_forwarded" {
		t.Fatalf("sync=%q %v", status, err)
	}
	if len(updates) != 1 || updates[0].OldSHA != "before" || updates[0].NewSHA != "after" {
		t.Fatalf("updates=%+v", updates)
	}
	if materialized != "**/profile.cov\n" || string(stdin) != "./profile.cov\x00" {
		t.Fatalf("target rules=%q stdin=%q", materialized, stdin)
	}
	if _, statErr := os.Stat(scratch); !os.IsNotExist(statErr) {
		t.Fatalf("scratch directory survived: %v", statErr)
	}
}

func TestCanonicalSyncBlocksTrackedDirtBeforeFetching(t *testing.T) {
	t.Parallel()
	f := runnertest.New(t)
	expectCanonicalSyncHead(f, "main", "?? profile.cov\n M README.md\n")
	status, err := syncCanonicalMergeTargetWithRunner(t.Context(), f, "/private/canonical", "main", "landing", 0, 0, nil)
	if status != "blocked_dirty" || err == nil || !strings.Contains(err.Error(), "worktree is dirty: ?? profile.cov\n M README.md") {
		t.Fatalf("tracked dirt=%q %v", status, err)
	}
	if len(f.Calls()) != 2 {
		t.Fatalf("observed beyond status: %+v", f.Calls())
	}
}

func TestCanonicalSyncBlocksUntrackedPathTheTargetDoesNotIgnore(t *testing.T) {
	t.Parallel()
	f := runnertest.New(t)
	expectCanonicalSyncHead(f, "main", "?? notes.txt\n")
	expectSyncGit(f, "before\n", nil, "rev-parse", "--verify", "HEAD^{commit}")
	expectSyncGit(f, "", nil, "fetch", "--no-tags", "origin", "+refs/heads/main:"+ignoredSyncRef)
	expectIgnoredSyncPrelude(f, "notes.txt\x00", "**/profile.cov\n")
	expectCheckIgnore(f, runner.Result{Stdout: "\x00\x00\x00notes.txt\x00", ExitCode: 1}, errors.New("exit status 1"), nil)
	status, err := syncCanonicalMergeTargetWithRunner(t.Context(), f, "/private/canonical", "main", "landing", 0, 0, nil)
	if status != "blocked_dirty" || err == nil || !strings.Contains(err.Error(), "untracked paths are not ignored by "+ignoredSyncRef+": notes.txt") {
		t.Fatalf("unignored dirt=%q %v", status, err)
	}
}

func TestCanonicalSyncReportsDirtStillPresentAfterFastForward(t *testing.T) {
	t.Parallel()
	f := runnertest.New(t)
	expectCanonicalSyncHead(f, "main", "?? profile.cov\n")
	expectSyncGit(f, "before\n", nil, "rev-parse", "--verify", "HEAD^{commit}")
	expectSyncGit(f, "", nil, "fetch", "--no-tags", "origin", "+refs/heads/main:"+ignoredSyncRef)
	expectIgnoredSyncPrelude(f, "profile.cov\x00", "**/profile.cov\n")
	expectCheckIgnore(f, runner.Result{Stdout: ".gitignore\x001\x00**/profile.cov\x00profile.cov\x00"}, nil, nil)
	expectSyncGit(f, "", nil, "merge", "--ff-only", ignoredSyncRef)
	expectSyncGit(f, "after\n", nil, "rev-parse", "--verify", "HEAD^{commit}")
	expectSyncGit(f, "landing\n", nil, "merge-base", "landing", "after")
	expectSyncGit(f, "?? late.txt\n", nil, "status", "--porcelain=v1")
	status, err := syncCanonicalMergeTargetWithRunner(t.Context(), f, "/private/canonical", "main", "landing", 0, 0, nil)
	if status != "blocked_dirty" || err == nil || !strings.Contains(err.Error(), "fast-forwarded to after but is still not clean") {
		t.Fatalf("late dirt=%q %v", status, err)
	}
}

func TestUntrackedDirtWithNothingListedNeedsNoTargetEvaluation(t *testing.T) {
	t.Parallel()
	f := runnertest.New(t)
	expectSyncGit(f, "", nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err := requireUntrackedDirtIgnoredAtTarget(t.Context(), f, "/c", ignoredSyncRef, ""); err != nil {
		t.Fatal(err)
	}
}

func TestUntrackedDirtTouchedByTheFastForwardBlocks(t *testing.T) {
	t.Parallel()
	for name, changed := range map[string]string{"same path": "a.txt\x00", "target adds a file over its directory": "dir\x00", "target adds files inside it": "nested/inner.go\x00"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := runnertest.New(t)
			expectSyncGit(f, "a.txt\x00dir/x.cov\x00nested/\x00", nil, "ls-files", "--others", "--exclude-standard", "-z")
			expectSyncGit(f, "unrelated\x00"+changed, nil, "diff", "--name-only", "--no-renames", "-z", "HEAD", ignoredSyncRef)
			err := requireUntrackedDirtIgnoredAtTarget(t.Context(), f, "/c", ignoredSyncRef, "")
			if err == nil || !strings.Contains(err.Error(), "untracked paths are changed by the fast-forward") {
				t.Fatalf("touched=%v", err)
			}
		})
	}
}

func TestUntrackedDirtEvaluationRefusesOnEveryFailedObservation(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("observation refused")
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, scratchRoot, want string
		script                  func(*runnertest.Fake)
	}{
		{"list untracked", "", "observation refused", func(f *runnertest.Fake) {
			expectSyncGit(f, "", sentinel, "ls-files", "--others", "--exclude-standard", "-z")
		}},
		{"diff target", "", "observation refused", func(f *runnertest.Fake) {
			expectSyncGit(f, "a\x00", nil, "ls-files", "--others", "--exclude-standard", "-z")
			expectSyncGit(f, "", sentinel, "diff", "--name-only", "--no-renames", "-z", "HEAD", ignoredSyncRef)
		}},
		{"git dir", "", "observation refused", func(f *runnertest.Fake) {
			expectSyncGit(f, "a\x00", nil, "ls-files", "--others", "--exclude-standard", "-z")
			expectSyncGit(f, "", nil, "diff", "--name-only", "--no-renames", "-z", "HEAD", ignoredSyncRef)
			expectSyncGit(f, "", sentinel, "rev-parse", "--absolute-git-dir")
		}},
		{"scratch directory", notDir, "create ignore scratch directory", func(f *runnertest.Fake) {
			expectSyncGit(f, "a\x00", nil, "ls-files", "--others", "--exclude-standard", "-z")
			expectSyncGit(f, "", nil, "diff", "--name-only", "--no-renames", "-z", "HEAD", ignoredSyncRef)
			expectSyncGit(f, "/g\n", nil, "rev-parse", "--absolute-git-dir")
		}},
		{"list target ignore files", "", "observation refused", func(f *runnertest.Fake) {
			expectSyncGit(f, "a\x00", nil, "ls-files", "--others", "--exclude-standard", "-z")
			expectSyncGit(f, "", nil, "diff", "--name-only", "--no-renames", "-z", "HEAD", ignoredSyncRef)
			expectSyncGit(f, "/g\n", nil, "rev-parse", "--absolute-git-dir")
			expectSyncGit(f, "", sentinel, "--literal-pathspecs", "ls-tree", "-z", ignoredSyncRef, "--", ".gitignore")
		}},
		{"malformed ls-tree entry", "", "unexpected ls-tree entry", func(f *runnertest.Fake) {
			expectSyncGit(f, "a\x00", nil, "ls-files", "--others", "--exclude-standard", "-z")
			expectSyncGit(f, "", nil, "diff", "--name-only", "--no-renames", "-z", "HEAD", ignoredSyncRef)
			expectSyncGit(f, "/g\n", nil, "rev-parse", "--absolute-git-dir")
			expectSyncGit(f, "garbage\x00", nil, "--literal-pathspecs", "ls-tree", "-z", ignoredSyncRef, "--", ".gitignore")
		}},
		{"read target ignore file", "", "observation refused", func(f *runnertest.Fake) {
			expectIgnoredSyncPreludeFailingRead(f, "100644 blob abc\t.gitignore\x00", sentinel)
		}},
		{"materialize nested directory", "", "materialize a/.gitignore", func(f *runnertest.Fake) {
			expectSyncGit(f, "a/b\x00", nil, "ls-files", "--others", "--exclude-standard", "-z")
			expectSyncGit(f, "", nil, "diff", "--name-only", "--no-renames", "-z", "HEAD", ignoredSyncRef)
			expectSyncGit(f, "/g\n", nil, "rev-parse", "--absolute-git-dir")
			expectSyncGit(f, "100644 blob one\ta\x00100644 blob two\ta/.gitignore\x00", nil, "--literal-pathspecs", "ls-tree", "-z", ignoredSyncRef, "--", ".gitignore", "a/.gitignore")
			expectSyncGit(f, "x", nil, "cat-file", "blob", "one")
			expectSyncGit(f, "y", nil, "cat-file", "blob", "two")
		}},
		{"materialize file", "", "materialize .gitignore", func(f *runnertest.Fake) {
			expectSyncGit(f, "a\x00", nil, "ls-files", "--others", "--exclude-standard", "-z")
			expectSyncGit(f, "", nil, "diff", "--name-only", "--no-renames", "-z", "HEAD", ignoredSyncRef)
			expectSyncGit(f, "/g\n", nil, "rev-parse", "--absolute-git-dir")
			expectSyncGit(f, "100644 blob one\t.gitignore/x\x00100644 blob two\t.gitignore\x00", nil, "--literal-pathspecs", "ls-tree", "-z", ignoredSyncRef, "--", ".gitignore")
			expectSyncGit(f, "x", nil, "cat-file", "blob", "one")
			expectSyncGit(f, "y", nil, "cat-file", "blob", "two")
		}},
		{"check ignore failure", "", "observation refused", func(f *runnertest.Fake) {
			expectIgnoredSyncPrelude(f, "a\x00", "a\n")
			expectCheckIgnore(f, runner.Result{ExitCode: 128}, sentinel, nil)
		}},
		{"malformed check ignore report", "", "unexpected check-ignore output with 2 fields", func(f *runnertest.Fake) {
			expectIgnoredSyncPrelude(f, "a\x00", "a\n")
			expectCheckIgnore(f, runner.Result{Stdout: "x\x00y\x00"}, nil, nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := runnertest.New(t)
			tc.script(f)
			err := requireUntrackedDirtIgnoredAtTarget(t.Context(), f, "/c", ignoredSyncRef, tc.scratchRoot)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s=%v", tc.name, err)
			}
		})
	}
}

func expectIgnoredSyncPreludeFailingRead(f *runnertest.Fake, listing string, err error) {
	expectSyncGit(f, "a\x00", nil, "ls-files", "--others", "--exclude-standard", "-z")
	expectSyncGit(f, "", nil, "diff", "--name-only", "--no-renames", "-z", "HEAD", ignoredSyncRef)
	expectSyncGit(f, "/g\n", nil, "rev-parse", "--absolute-git-dir")
	expectSyncGit(f, listing, nil, "--literal-pathspecs", "ls-tree", "-z", ignoredSyncRef, "--", ".gitignore")
	expectSyncGit(f, "", err, "cat-file", "blob", "abc")
}

func TestTargetIgnoreRulesSkipSymlinksAndHonourNegation(t *testing.T) {
	t.Parallel()
	f := runnertest.New(t)
	expectSyncGit(f, "keep.cov\x00sub/drop.cov\x00", nil, "ls-files", "--others", "--exclude-standard", "-z")
	expectSyncGit(f, "", nil, "diff", "--name-only", "--no-renames", "-z", "HEAD", ignoredSyncRef)
	expectSyncGit(f, "/g\n", nil, "rev-parse", "--absolute-git-dir")
	expectSyncGit(f, "120000 blob link\t.gitignore\x00040000 tree t\tsub/.gitignore\x00", nil, "--literal-pathspecs", "ls-tree", "-z", ignoredSyncRef, "--", ".gitignore", "sub/.gitignore")
	expectCheckIgnore(f, runner.Result{Stdout: "x\x001\x00!keep.cov\x00keep.cov\x00x\x002\x00*.cov\x00sub/drop.cov\x00"}, nil, nil)
	err := requireUntrackedDirtIgnoredAtTarget(t.Context(), f, "/c", ignoredSyncRef, "")
	if err == nil || !strings.HasSuffix(err.Error(), "untracked paths are not ignored by "+ignoredSyncRef+": keep.cov") {
		t.Fatalf("negation=%v", err)
	}
}

func TestSyncIgnoreGitTreatsNonZeroExitWithoutErrorAsFailure(t *testing.T) {
	t.Parallel()
	f := runnertest.New(t)
	f.ExpectArgv([]string{"git", "status"}, runner.Result{ExitCode: 2, Stderr: "boom\n"}, nil)
	if _, err := syncIgnoreGit(t.Context(), f, "/c", nil, true, "status"); err == nil || !strings.Contains(err.Error(), "exit status 2: boom") {
		t.Fatalf("exit=%v", err)
	}
}

func TestStatusIsUntrackedOnlyClassifiesEveryEntry(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bool{"?? a\n?? b/\n": true, "?? a\nA  b\n": false, " M a\n": false, "UU a\n": false} {
		if got := statusIsUntrackedOnly(status); got != want {
			t.Fatalf("%q=%t", status, got)
		}
	}
}
