package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// statusArgv, diffShortstatArgv and revListCountArgv build the exact argv
// summarizeChanges' three gitOutput calls send, so a script matches by argv
// rather than by a loose predicate.

func statusArgv(dir string) []string {
	return []string{"git", "-C", dir, "status", "--porcelain"}
}

func diffShortstatArgv(dir, baseSHA string) []string {
	return []string{"git", "-C", dir, "diff", "--shortstat", baseSHA}
}

func revListCountArgv(dir, baseSHA string) []string {
	return []string{"git", "-C", dir, "rev-list", "--count", baseSHA + "..HEAD"}
}

func TestSummarizeChangesReturnsNilForABlankWorktreeDirectory(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	if summary := summarizeChanges(context.Background(), fake, "   ", "abc"); summary != nil {
		t.Fatalf("summary = %#v, want nil (no git call should even be attempted)", summary)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("CallCount() = %d, want 0", fake.CallCount())
	}
}

func TestSummarizeChangesCountsTheArtefactAndNotAReview(t *testing.T) {
	t.Parallel()
	dir, base := "/repo", "base-sha"
	fake := runnertest.New(t)
	fake.ExpectArgv(statusArgv(dir), runner.Result{Stdout: " M tracked.txt\n?? untracked.txt\n"}, nil)
	fake.ExpectArgv(diffShortstatArgv(dir, base), runner.Result{Stdout: " 1 file changed, 2 insertions(+), 1 deletion(-)\n"}, nil)
	fake.ExpectArgv(revListCountArgv(dir, base), runner.Result{Stdout: "0\n"}, nil)

	summary := summarizeChanges(context.Background(), fake, dir, base)
	if summary == nil {
		t.Fatal("a worktree with changes must produce a summary")
	}
	if summary.FilesChanged != 2 {
		t.Fatalf("files changed = %d, want 2 (tracked and untracked)", summary.FilesChanged)
	}
	joined := strings.Join(summary.Files, ",")
	if !strings.Contains(joined, "tracked.txt") || !strings.Contains(joined, "untracked.txt") {
		t.Fatalf("changed files = %#v", summary.Files)
	}
	if summary.Insertions != 2 || summary.Deletions != 1 {
		t.Fatalf("line counts = +%d/-%d, want +2/-1", summary.Insertions, summary.Deletions)
	}
	if summary.Commits != 0 {
		t.Fatalf("commits = %d, want 0 before the worker commits anything", summary.Commits)
	}
}

func TestSummarizeChangesCountsCommitsTheWorkerMade(t *testing.T) {
	t.Parallel()
	dir, base := "/repo", "base-sha"
	fake := runnertest.New(t)
	fake.ExpectArgv(statusArgv(dir), runner.Result{Stdout: ""}, nil)
	fake.ExpectArgv(diffShortstatArgv(dir, base), runner.Result{Stdout: ""}, nil)
	fake.ExpectArgv(revListCountArgv(dir, base), runner.Result{Stdout: "1\n"}, nil)

	summary := summarizeChanges(context.Background(), fake, dir, base)
	if summary == nil || summary.Commits != 1 {
		t.Fatalf("summary = %#v, want one commit", summary)
	}
}

func TestSummarizeChangesSkipsDiffAndRevListWhenBaseSHAIsBlank(t *testing.T) {
	t.Parallel()
	dir := "/repo"
	fake := runnertest.New(t)
	fake.ExpectArgv(statusArgv(dir), runner.Result{Stdout: ""}, nil)

	summary := summarizeChanges(context.Background(), fake, dir, "  ")
	if summary == nil {
		t.Fatal("expected a summary")
	}
	if fake.CallCount() != 1 {
		t.Fatalf("CallCount() = %d, want 1 (status only; no base to diff against)", fake.CallCount())
	}
}

func TestSummarizeChangesDegradesEachGitCallIndependently(t *testing.T) {
	t.Parallel()
	dir, base := "/repo", "not-a-revision"
	gitFailure := fmt.Errorf("exit status 128")

	t.Run("status fails", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		fake.ExpectArgv(statusArgv(dir), runner.Result{ExitCode: 128}, gitFailure)
		fake.ExpectArgv(diffShortstatArgv(dir, base), runner.Result{Stdout: ""}, nil)
		fake.ExpectArgv(revListCountArgv(dir, base), runner.Result{Stdout: "0\n"}, nil)

		summary := summarizeChanges(context.Background(), fake, dir, base)
		if summary == nil {
			t.Fatal("a git failure must still yield a (empty) summary object, never nil")
		}
		if summary.FilesChanged != 0 || len(summary.Files) != 0 {
			t.Fatalf("summary = %#v, want no files when status failed", summary)
		}
	})

	t.Run("diff --shortstat fails", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		fake.ExpectArgv(statusArgv(dir), runner.Result{Stdout: "?? extra.txt\n"}, nil)
		fake.ExpectArgv(diffShortstatArgv(dir, base), runner.Result{ExitCode: 128}, gitFailure)
		fake.ExpectArgv(revListCountArgv(dir, base), runner.Result{Stdout: "0\n"}, nil)

		summary := summarizeChanges(context.Background(), fake, dir, base)
		if summary == nil || summary.FilesChanged != 1 {
			t.Fatalf("summary = %#v, want the porcelain-derived count unaffected by an unusable base revision", summary)
		}
		if summary.Insertions != 0 || summary.Deletions != 0 {
			t.Fatalf("summary = %#v, want zero line counts when diff --shortstat failed", summary)
		}
	})

	t.Run("rev-list --count fails", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		fake.ExpectArgv(statusArgv(dir), runner.Result{Stdout: ""}, nil)
		fake.ExpectArgv(diffShortstatArgv(dir, base), runner.Result{Stdout: ""}, nil)
		fake.ExpectArgv(revListCountArgv(dir, base), runner.Result{ExitCode: 128}, gitFailure)

		summary := summarizeChanges(context.Background(), fake, dir, base)
		if summary == nil || summary.Commits != 0 {
			t.Fatalf("summary = %#v, want zero commits when rev-list --count failed", summary)
		}
	})

	t.Run("rev-list --count prints something unparsable", func(t *testing.T) {
		t.Parallel()
		fake := runnertest.New(t)
		fake.ExpectArgv(statusArgv(dir), runner.Result{Stdout: ""}, nil)
		fake.ExpectArgv(diffShortstatArgv(dir, base), runner.Result{Stdout: ""}, nil)
		fake.ExpectArgv(revListCountArgv(dir, base), runner.Result{Stdout: "not-a-number\n"}, nil)

		summary := summarizeChanges(context.Background(), fake, dir, base)
		if summary == nil || summary.Commits != 0 {
			t.Fatalf("summary = %#v, want zero commits for an unparsable count rather than a poisoned value", summary)
		}
	})
}

func TestSummarizeChangesBoundsTheChangedFileList(t *testing.T) {
	t.Parallel()
	dir, base := "/repo", "base-sha"
	var status strings.Builder
	for index := 0; index < maxChangedFiles+5; index++ {
		fmt.Fprintf(&status, " M file-%03d.txt\n", index)
	}
	fake := runnertest.New(t)
	fake.ExpectArgv(statusArgv(dir), runner.Result{Stdout: status.String()}, nil)
	fake.ExpectArgv(diffShortstatArgv(dir, base), runner.Result{Stdout: ""}, nil)
	fake.ExpectArgv(revListCountArgv(dir, base), runner.Result{Stdout: "0\n"}, nil)

	summary := summarizeChanges(context.Background(), fake, dir, base)
	if summary == nil {
		t.Fatal("expected a summary")
	}
	if len(summary.Files) != maxChangedFiles || !summary.Truncated {
		t.Fatalf("file list must be capped and flagged: %d truncated=%v", len(summary.Files), summary.Truncated)
	}
	if summary.FilesChanged <= maxChangedFiles {
		t.Fatalf("the count must stay exact even when the list is capped: %d", summary.FilesChanged)
	}
}

func TestParseShortstatReadsGitOutput(t *testing.T) {
	t.Parallel()
	cases := map[string][2]int{
		" 3 files changed, 12 insertions(+), 4 deletions(-)": {12, 4},
		" 1 file changed, 2 insertions(+)":                   {2, 0},
		" 1 file changed, 1 deletion(-)":                     {0, 1},
		"":                                                   {0, 0},
		" nonsense, 4 deletions(-)":                          {0, 4},
	}
	for input, want := range cases {
		insertions, deletions := parseShortstat(input)
		if insertions != want[0] || deletions != want[1] {
			t.Errorf("parseShortstat(%q) = %d/%d, want %d/%d", input, insertions, deletions, want[0], want[1])
		}
	}
}

func TestGitOutputReturnsTrimmedStdoutOnSuccess(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "-C", "/repo", "rev-parse", "HEAD"}, runner.Result{Stdout: "deadbeef\n"}, nil)

	output, err := gitOutput(context.Background(), fake, "/repo", "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("gitOutput: %v", err)
	}
	if output != "deadbeef\n" {
		t.Fatalf("output = %q, want the runner's raw stdout unmodified", output)
	}
}

func TestGitOutputReportsFailures(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	wantErr := errors.New("exit status 128")
	fake.ExpectArgv([]string{"git", "-C", "/repo", "rev-parse", "HEAD"}, runner.Result{ExitCode: 128}, wantErr)

	_, err := gitOutput(context.Background(), fake, "/repo", "rev-parse", "HEAD")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to wrap %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "git rev-parse HEAD in /repo") {
		t.Fatalf("err = %v, want it to name the argv and directory", err)
	}
}

func TestGitOutputRunsWithARestrictedEnvironment(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	var seen []string
	fake.Expect(func(c runnertest.Call) bool {
		seen = c.Opts.Env
		return c.Name == "git"
	}, runner.Result{Stdout: "ok"}, nil)

	if _, err := gitOutput(context.Background(), fake, "/repo", "status"); err != nil {
		t.Fatalf("gitOutput: %v", err)
	}
	joined := strings.Join(seen, "\n")
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env = %v, want it to contain %q", seen, want)
		}
	}
}
