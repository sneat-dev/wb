//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

type historyProofGitReply struct {
	args   []string
	output string
	err    error
}

func historyProofGitScript(t *testing.T, root string, replies ...historyProofGitReply) context.Context {
	t.Helper()
	fake := runnertest.New(t)
	for _, reply := range replies {
		argv := append([]string{"git", "-C", root}, reply.args...)
		fake.ExpectArgv(argv, runner.Result{CombinedOutput: reply.output}, reply.err)
	}
	t.Cleanup(func() {
		calls := fake.Calls()
		if len(calls) != len(replies) {
			t.Errorf("Git queries = %d, want %d; calls = %#v", len(calls), len(replies), calls)
		}
		for index := 0; index < len(calls) && index < len(replies); index++ {
			want := append([]string{"git", "-C", root}, replies[index].args...)
			if calls[index].Dir != root || !slices.Equal(calls[index].Argv(), want) {
				t.Errorf("Git query %d = %q in %q, want %q in %q", index, calls[index].Argv(), calls[index].Dir, want, root)
			}
		}
	})
	return withGitRunner(context.Background(), fake)
}

func historyProofReply(output string, args ...string) historyProofGitReply {
	return historyProofGitReply{args: args, output: output}
}

// These pure inputs describe the exact syntax admitted by the rebase proof.
func TestE2EHistoryProofRejectsMalformedTodoAndCounters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false}, {"0", false}, {"000", false}, {"1", true}, {"0012", true}, {"1x", false}, {"-1", false},
	} {
		if got := positiveDecimal(tc.value); got != tc.want {
			t.Errorf("positiveDecimal(%q) = %t, want %t", tc.value, got, tc.want)
		}
	}
	for _, tc := range []struct {
		value string
		limit string
		want  bool
	}{
		{"2", "10", true}, {"10", "2", false}, {"002", "02", true}, {"03", "002", false},
	} {
		if got := decimalAtMost(tc.value, tc.limit); got != tc.want {
			t.Errorf("decimalAtMost(%q, %q) = %t, want %t", tc.value, tc.limit, got, tc.want)
		}
	}
	for _, todo := range []string{"pick invalid\n", "pick\n", "arbitrary deadbeef\n", "exec echo harmless\n"} {
		if rebaseTodoHasResolvableCommit(ctx, "/unqueried", todo) {
			t.Errorf("invalid or commit-free todo accepted: %q", todo)
		}
	}
}

func TestE2EHistoryProofReflogRequiresExactGitEvidence(t *testing.T) {
	t.Parallel()
	const root = "/history-proof-fixture"
	const expected = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	boom := errors.New("Git query failed")
	for _, tc := range []struct {
		name    string
		replies []historyProofGitReply
		want    bool
	}{
		{"Git error", []historyProofGitReply{{args: []string{"reflog", "show", "-1", "--format=%H%x00%gs", "HEAD"}, err: boom}}, false},
		{"missing checkout subject", []historyProofGitReply{historyProofReply(expected+"\x00commit: unrelated", "reflog", "show", "-1", "--format=%H%x00%gs", "HEAD")}, false},
		{"missing destination separator", []historyProofGitReply{historyProofReply(expected+"\x00checkout: moving from main", "reflog", "show", "-1", "--format=%H%x00%gs", "HEAD")}, false},
		{"exact destination", []historyProofGitReply{
			historyProofReply(expected+"\x00checkout: moving from main to feature", "reflog", "show", "-1", "--format=%H%x00%gs", "HEAD"),
			historyProofReply(expected, "rev-parse", "--verify", "feature^{commit}"),
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := historyProofGitScript(t, root, tc.replies...)
			if got := reflogShowsCheckoutTo(ctx, root, expected); got != tc.want {
				t.Fatalf("checkout reflog proof = %t, want %t", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		reply historyProofGitReply
		want  bool
	}{
		{"Git error", historyProofGitReply{args: []string{"reflog", "show", "--format=%H%x00%gs", "HEAD"}, err: boom}, false},
		{"malformed entry", historyProofReply(expected+" no separator", "reflog", "show", "--format=%H%x00%gs", "HEAD"), false},
		{"no rebase start", historyProofReply(expected+"\x00commit (amend): feature", "reflog", "show", "--format=%H%x00%gs", "HEAD"), false},
		{"wrong operation", historyProofReply(expected+"\x00checkout: moving from main to feature", "reflog", "show", "--format=%H%x00%gs", "HEAD"), false},
		{"contiguous rebase", historyProofReply(expected+"\x00commit (amend): feature\n"+expected+"\x00rebase (start): checkout main", "reflog", "show", "--format=%H%x00%gs", "HEAD"), true},
	} {
		t.Run("active "+tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := historyProofGitScript(t, root, tc.reply)
			if got := reflogShowsActiveRebase(ctx, root, expected); got != tc.want {
				t.Fatalf("active rebase reflog proof = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestE2EHistoryProofBisectRequiresEveryLiveCorroboration(t *testing.T) {
	t.Parallel()
	const root = "/scripted-bisect-checkout"
	const expected = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const good = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, tc := range []struct {
		name string
		stop string
		want bool
	}{
		{"invalid expected commit", "expected", false},
		{"missing bisect terms", "terms", false},
		{"checked out wrong commit", "head", false},
		{"no good ref", "good refs", false},
		{"malformed good ref", "good commit", false},
		{"complete live bisect", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gitDir := t.TempDir()
			for name, content := range map[string]string{
				"BISECT_START":        "feature\n",
				"BISECT_EXPECTED_REV": expected + "\n",
				"BISECT_LOG":          "git bisect start\n",
				"BISECT_NAMES":        "",
				"BISECT_TERMS":        "bad\ngood\n",
			} {
				if err := os.WriteFile(filepath.Join(gitDir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			replies := []historyProofGitReply{historyProofReply("", "show-ref", "--verify", "--quiet", "refs/heads/feature")}
			if tc.stop == "expected" {
				if err := os.WriteFile(filepath.Join(gitDir, "BISECT_EXPECTED_REV"), []byte("not-a-commit\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				replies = append(replies, historyProofReply(expected, "rev-parse", "--verify", "--quiet", expected+"^{commit}"))
			}
			if tc.stop == "terms" {
				if err := os.Remove(filepath.Join(gitDir, "BISECT_TERMS")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.stop != "expected" && tc.stop != "terms" {
				head := expected
				if tc.stop == "head" {
					head = good
				}
				replies = append(replies, historyProofReply(head, "rev-parse", "--verify", "HEAD^{commit}"))
			}
			if tc.stop != "expected" && tc.stop != "terms" && tc.stop != "head" {
				replies = append(replies, historyProofReply("", "show-ref", "--verify", "--quiet", "refs/bisect/bad"))
				goodRefs := good
				switch tc.stop {
				case "good refs":
					goodRefs = ""
				case "good commit":
					goodRefs = "invalid"
				}
				replies = append(replies, historyProofReply(goodRefs, "for-each-ref", "--format=%(objectname)", "refs/bisect/good-*"))
			}
			if tc.want {
				replies = append(replies,
					historyProofReply(good, "rev-parse", "--verify", "--quiet", good+"^{commit}"),
					historyProofReply("git bisect start", "bisect", "log"),
					historyProofReply(expected+"\x00checkout: moving from feature to bisect", "reflog", "show", "-1", "--format=%H%x00%gs", "HEAD"),
					historyProofReply(expected, "rev-parse", "--verify", "bisect^{commit}"),
				)
			}
			ctx := historyProofGitScript(t, root, replies...)
			if got := bisectInProgress(ctx, root, gitDir); got != tc.want {
				t.Fatalf("bisect proof = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestE2EHistoryProofRefusesUnreadableRegularState(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "CHERRY_PICK_HEAD")
	if err := os.WriteFile(file, []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o600) })
	if _, err := os.ReadFile(file); err == nil {
		t.Skip("current user can read a mode-000 file; permission refusal is unavailable")
	}
	if value, ok := regularStateFile(file, true); ok || value != "" {
		t.Fatalf("unreadable regular marker = %q, %t", value, ok)
	}
	state := t.TempDir()
	headName := filepath.Join(state, "head-name")
	if err := os.WriteFile(headName, []byte("refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(headName, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(headName, 0o600) })
	if coherentRebaseState(context.Background(), "/unqueried", state, []string{"head-name"}, true) {
		t.Fatal("unreadable regular rebase state accepted")
	}
}

func TestE2EHistoryProofStateFilesRequireRegularEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	regular := filepath.Join(root, "marker")
	if err := os.WriteFile(regular, []byte("  commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, ok := regularStateFile(regular, true); !ok || value != "commit" {
		t.Fatalf("regular marker = %q, %t", value, ok)
	}
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := regularStateFile(regular, true); ok {
		t.Fatal("empty required marker accepted")
	}
	if value, ok := regularStateFile(regular, false); !ok || value != "" {
		t.Fatalf("empty existence-only marker = %q, %t", value, ok)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	redirected := filepath.Join(root, "redirected")
	if err := os.Symlink(outside, redirected); err != nil {
		t.Fatal(err)
	}
	if _, ok := regularStateFile(redirected, true); ok {
		t.Fatal("symlinked marker accepted")
	}
	if _, ok := regularStateFile(root, false); ok {
		t.Fatal("directory accepted as marker")
	}
	if after, err := os.ReadFile(outside); err != nil || string(after) != "outside bytes\n" {
		t.Fatalf("redirected target changed: %q, %v", after, err)
	}
}

func TestE2EHistoryProofMergeTodoRequiresCommitUntilFinalStep(t *testing.T) {
	t.Parallel()
	const root = "/scripted-merge-rebase"
	const commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	state := t.TempDir()
	todo := filepath.Join(state, "git-rebase-todo")
	if err := os.WriteFile(todo, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	backup := "pick " + commit + " feature\n"
	query := historyProofReply(commit, "rev-parse", "--verify", "--quiet", commit+"^{commit}")
	if coherentMergeRebaseTodo(historyProofGitScript(t, root, query), root, state, backup, "1", "2") {
		t.Fatal("empty active todo accepted before the last rebase step")
	}
	if !coherentMergeRebaseTodo(historyProofGitScript(t, root, query), root, state, backup, "2", "2") {
		t.Fatal("Git's empty active todo was rejected at the last step")
	}
	if coherentMergeRebaseTodo(context.Background(), root, state, backup, "0", "2") {
		t.Fatal("zero message number accepted")
	}
	if err := os.Chmod(todo, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(todo, 0o600) })
	if _, err := os.ReadFile(todo); err == nil {
		t.Skip("current user can read a mode-000 todo; permission refusal is unavailable")
	}
	if coherentMergeRebaseTodo(historyProofGitScript(t, root, query), root, state, backup, "1", "2") {
		t.Fatal("unreadable active todo accepted")
	}
}

func historyProofCreatedWorktree(t *testing.T, operation string) (*gitFixture, string, string) {
	t.Helper()
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: operation, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	return fixture, worktree, gitTestOutput(t, worktree, "rev-parse", "--absolute-git-dir")
}

func historyProofRefusalKeepsCheckout(t *testing.T, fixture *gitFixture, worktree, statePath string) {
	t.Helper()
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	branches := gitTestOutput(t, fixture.canonical, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("tampered history guard = %#v, %v", got, err)
	}
	if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != head {
		t.Fatalf("guard moved HEAD: %s -> %s", head, got)
	}
	if got := gitTestOutput(t, fixture.canonical, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"); got != branches {
		t.Fatalf("guard moved branches: %q -> %q", branches, got)
	}
	after, err := os.ReadFile(statePath)
	if err != nil || string(after) != string(state) {
		t.Fatalf("guard altered state file %s: %q, %v", statePath, after, err)
	}
}

//nolint:paralleltest // newGitFixture uses t.Setenv for this real-Git paused cherry-pick fixture.
func TestE2EHistoryProofPausedCherryPickNeedsMergeMessage(t *testing.T) {
	fixture, worktree, gitDir := historyProofCreatedWorktree(t, "history-cherry")
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("picked side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "README.md")
	gitTest(t, worktree, "commit", "-m", "picked side")
	picked := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	gitTest(t, worktree, "reset", "--hard", "HEAD~1")
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("current side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "README.md")
	gitTest(t, worktree, "commit", "-m", "current side")
	gitTest(t, worktree, "checkout", "--detach", "HEAD")
	if output, err := gitTestRun(worktree, "cherry-pick", picked); err == nil {
		t.Fatalf("expected a paused cherry-pick conflict, got %q", output)
	}
	if got, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil || !got.Transient || got.TransientOperation != "cherry-pick" {
		t.Fatalf("native cherry-pick guard = %#v, %v", got, err)
	}
	message := filepath.Join(gitDir, "MERGE_MSG")
	if err := os.Remove(message); err != nil {
		t.Fatal(err)
	}
	historyProofRefusalKeepsCheckout(t, fixture, worktree, filepath.Join(gitDir, "CHERRY_PICK_HEAD"))
	if _, err := os.Lstat(message); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("guard changed absent cherry-pick message: %v", err)
	}
}

//nolint:paralleltest // newGitFixture uses t.Setenv for this real-Git paused bisect fixture.
func TestE2EHistoryProofPausedBisectRejectsIncompleteState(t *testing.T) {
	fixture, worktree, gitDir := historyProofCreatedWorktree(t, "history-bisect")
	for index := 0; index < 4; index++ {
		name := fmt.Sprintf("bisect-%d.txt", index)
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitTest(t, worktree, "add", name)
		gitTest(t, worktree, "commit", "-m", name)
	}
	gitTest(t, worktree, "bisect", "start", "HEAD", "HEAD~4")
	if got, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil || !got.Transient || got.TransientOperation != "bisect" {
		t.Fatalf("native bisect guard = %#v, %v", got, err)
	}
	start := filepath.Join(gitDir, "BISECT_START")
	original, err := os.ReadFile(start)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(start, []byte("refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	historyProofRefusalKeepsCheckout(t, fixture, worktree, start)
	if err := os.WriteFile(start, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil || !got.Transient || got.TransientOperation != "bisect" {
		t.Fatalf("restored bisect guard = %#v, %v", got, err)
	}
	if err := os.Remove(filepath.Join(gitDir, "BISECT_LOG")); err != nil {
		t.Fatal(err)
	}
	historyProofRefusalKeepsCheckout(t, fixture, worktree, start)
	if _, err := os.Lstat(filepath.Join(gitDir, "BISECT_LOG")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("guard changed absent bisect log: %v", err)
	}
}

//nolint:paralleltest // newGitFixture uses t.Setenv for both real paused rebase backends.
func TestE2EHistoryProofPausedRebaseRejectsBrokenState(t *testing.T) {
	for _, mode := range []struct {
		name  string
		args  []string
		state string
	}{
		{"merge", []string{"rebase", "origin/main"}, "rebase-merge"},
		{"apply", []string{"rebase", "--apply", "origin/main"}, "rebase-apply"},
	} {
		//nolint:paralleltest // each fixture changes HOME and XDG_CONFIG_HOME through t.Setenv.
		t.Run(mode.name, func(t *testing.T) {
			fixture, worktree, gitDir := historyProofCreatedWorktree(t, "history-rebase-"+mode.name)
			if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("feature\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitTest(t, worktree, "add", "README.md")
			gitTest(t, worktree, "commit", "-m", "feature")
			if err := os.WriteFile(filepath.Join(fixture.canonical, "README.md"), []byte("main\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitTest(t, fixture.canonical, "add", "README.md")
			gitTest(t, fixture.canonical, "commit", "-m", "main")
			gitTest(t, fixture.canonical, "push", "origin", "main")
			gitTest(t, worktree, "fetch", "origin", "main")
			if output, err := gitTestRun(worktree, mode.args...); err == nil {
				t.Fatalf("expected a paused %s rebase conflict, got %q", mode.name, output)
			}
			if got, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil || !got.Transient || got.TransientOperation != "rebase" {
				t.Fatalf("native %s rebase guard = %#v, %v", mode.name, got, err)
			}
			if mode.name == "merge" {
				// The conflict also has unmerged index entries. A second valid
				// cherry-pick-shaped marker must not outrank the real rebase.
				for name, content := range map[string]string{
					"CHERRY_PICK_HEAD": gitTestOutput(t, worktree, "rev-parse", "HEAD") + "\n",
					"MERGE_MSG":        "competing operation marker\n",
				} {
					if err := os.WriteFile(filepath.Join(gitDir, name), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if !cherryPickInProgress(context.Background(), worktree, gitDir) {
					t.Fatal("competing cherry-pick proof did not reach the priority boundary")
				}
				if operation := transientHistoryOperation(context.Background(), worktree, gitDir); operation != "rebase" {
					t.Fatalf("operation priority = %q, want rebase", operation)
				}
				for _, name := range []string{"CHERRY_PICK_HEAD", "MERGE_MSG"} {
					if err := os.Remove(filepath.Join(gitDir, name)); err != nil {
						t.Fatal(err)
					}
				}
			}
			state := filepath.Join(gitDir, mode.state)
			if mode.name == "merge" {
				headName := filepath.Join(state, "head-name")
				onto := filepath.Join(state, "onto")
				heldOnto := filepath.Join(state, "onto.held-for-test")
				if err := os.Rename(onto, heldOnto); err != nil {
					t.Fatal(err)
				}
				historyProofRefusalKeepsCheckout(t, fixture, worktree, headName)
				if _, err := os.Lstat(onto); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("guard changed missing required onto state: %v", err)
				}
				if err := os.Rename(heldOnto, onto); err != nil {
					t.Fatal(err)
				}
				if got, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil || !got.Transient || got.TransientOperation != "rebase" {
					t.Fatalf("restored required rebase state guard = %#v, %v", got, err)
				}

				originalHeadName, err := os.ReadFile(headName)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(headName, []byte("refs/tags/forged\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				historyProofRefusalKeepsCheckout(t, fixture, worktree, headName)
				if err := os.WriteFile(headName, originalHeadName, 0o644); err != nil {
					t.Fatal(err)
				}
				if got, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil || !got.Transient || got.TransientOperation != "rebase" {
					t.Fatalf("restored branch-bound rebase guard = %#v, %v", got, err)
				}
			}
			var damaged string
			if mode.name == "merge" {
				damaged = filepath.Join(state, "msgnum")
			} else {
				damaged = filepath.Join(state, "next")
			}
			original, err := os.ReadFile(damaged)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(damaged, []byte("not-a-counter\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			historyProofRefusalKeepsCheckout(t, fixture, worktree, damaged)
			if err := os.WriteFile(damaged, original, 0o644); err != nil {
				t.Fatal(err)
			}
			if got, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil || !got.Transient || got.TransientOperation != "rebase" {
				t.Fatalf("restored %s rebase guard = %#v, %v", mode.name, got, err)
			}
			if mode.name == "merge" {
				backup := filepath.Join(state, "git-rebase-todo.backup")
				if err := os.WriteFile(backup, []byte("pick invalid\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				historyProofRefusalKeepsCheckout(t, fixture, worktree, backup)
			}
		})
	}
}
