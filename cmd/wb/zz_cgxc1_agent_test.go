package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/agents"
)

// cov-cgx-c1 trial lane: covers cmd/wb/agent.go seams named in
// scratchpad/seams/rwi/c1.txt (selectWorktreeMode, readAgentTask,
// renderAgentResult, printAgentStatusText, finishAgentAwait, awaitAgentRun,
// printAgentAwaitText, indentContinuation, printAgentList, and the RunE
// closures guarding a nil remote Result).

func newCgxc1WorktreeModeCmd() *cobra.Command {
	command := &cobra.Command{}
	command.Flags().String("new-worktree", "", "")
	command.Flags().String("use-worktree", "", "")
	return command
}

func TestCgxc1SelectWorktreeModeCoversEveryBranch(t *testing.T) {
	t.Parallel()

	t.Run("both flags explicitly changed is a conflict", func(t *testing.T) {
		t.Parallel()
		command := newCgxc1WorktreeModeCmd()
		if err := command.Flags().Set("new-worktree", "a"); err != nil {
			t.Fatal(err)
		}
		if err := command.Flags().Set("use-worktree", "b"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := selectWorktreeMode(command, "a", "b"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("both values non-empty without Changed is still a conflict", func(t *testing.T) {
		t.Parallel()
		command := newCgxc1WorktreeModeCmd()
		if _, _, err := selectWorktreeMode(command, "a", "b"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("new only", func(t *testing.T) {
		t.Parallel()
		command := newCgxc1WorktreeModeCmd()
		mode, name, err := selectWorktreeMode(command, "created-name", "")
		if err != nil || mode != agents.ModeNew || name != "created-name" {
			t.Fatalf("mode=%q name=%q err=%v", mode, name, err)
		}
	})

	t.Run("existing only", func(t *testing.T) {
		t.Parallel()
		command := newCgxc1WorktreeModeCmd()
		mode, name, err := selectWorktreeMode(command, "", "existing-name")
		if err != nil || mode != agents.ModeExisting || name != "existing-name" {
			t.Fatalf("mode=%q name=%q err=%v", mode, name, err)
		}
	})

	t.Run("neither is required", func(t *testing.T) {
		t.Parallel()
		command := newCgxc1WorktreeModeCmd()
		if _, _, err := selectWorktreeMode(command, "", ""); err == nil || !strings.Contains(err.Error(), "exactly one of") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCgxc1ReadAgentTaskCoversTaskFileBranches(t *testing.T) {
	t.Parallel()

	t.Run("task and task-file together is a usage error", func(t *testing.T) {
		t.Parallel()
		command := &cobra.Command{}
		if _, err := readAgentTask(command, "inline", "/some/path"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("task-file - reads stdin", func(t *testing.T) {
		t.Parallel()
		command := &cobra.Command{}
		command.SetIn(strings.NewReader("do the thing\n"))
		text, err := readAgentTask(command, "", "-")
		if err != nil || text != "do the thing\n" {
			t.Fatalf("text=%q err=%v", text, err)
		}
	})

	t.Run("task-file - refuses empty stdin", func(t *testing.T) {
		t.Parallel()
		command := &cobra.Command{}
		command.SetIn(strings.NewReader("   \n"))
		if _, err := readAgentTask(command, "", "-"); err == nil || !strings.Contains(err.Error(), "non-empty task on stdin") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("neither task nor task-file is required", func(t *testing.T) {
		t.Parallel()
		command := &cobra.Command{}
		if _, err := readAgentTask(command, "", ""); err == nil || !strings.Contains(err.Error(), "one of --task or --task-file is required") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCgxc1RenderAgentResultWritesJSONWhenRequested(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var out strings.Builder
	command.SetOut(&out)
	result := agents.Result{AgentID: "agt-cgxc1", State: agents.StateCompleted}
	if err := renderAgentResult(command, true, result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "agt-cgxc1") {
		t.Fatalf("expected the JSON body to carry the agent id: %s", out.String())
	}
}

func TestCgxc1PrintAgentStatusTextCoversFailureAndWriteError(t *testing.T) {
	t.Parallel()
	result := agents.Result{AgentID: "agt-cgxc1", State: agents.StateFailed, Failure: "boom", StartedAt: time.Now()}
	var out strings.Builder
	if err := printAgentStatusText(&out, result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "failure:  boom") {
		t.Fatalf("missing failure line: %s", out.String())
	}

	sentinel := errors.New("stdout is closed")
	if err := printAgentStatusText(failingWriter{err: sentinel}, result); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

func TestCgxc1FinishAgentAwaitPropagatesJSONWriteError(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	sentinel := errors.New("stdout is closed")
	command.SetOut(failingWriter{err: sentinel})
	err := finishAgentAwait(command, true, agents.Result{AgentID: "agt-cgxc1", State: agents.StateCompleted, Terminal: true})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

func TestCgxc1AwaitAgentRunReturnsContextErrorWhenCancelled(t *testing.T) {
	home := agentTestEnv(t)
	store := agents.NewStore(home)
	record := seedAgentRun(t, home, func(r *agents.Record) { r.State = agents.StateRunning })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := awaitAgentRun(ctx, store, record.AgentID, time.Time{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want %v", err, context.Canceled)
	}
}

// cgxc1FailAfterWriter succeeds for the first `after` writes, then fails,
// so a test can isolate exactly one write call among several without
// depending on how many bytes each Fprint call sends.
type cgxc1FailAfterWriter struct {
	after int
	err   error
}

func (w *cgxc1FailAfterWriter) Write(p []byte) (int, error) {
	if w.after <= 0 {
		return 0, w.err
	}
	w.after--
	return len(p), nil
}

func TestCgxc1PrintAgentAwaitTextHappyPathCoversChangesAndResultLines(t *testing.T) {
	t.Parallel()
	result := agents.Result{
		AgentID: "agt-cgxc1", State: agents.StateCompleted, Terminal: true,
		Result:  "line one\nline two",
		Changes: &agents.ChangeSummary{Files: []string{"a.go", "b.go"}},
	}
	var out strings.Builder
	if err := printAgentAwaitText(&out, result); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"changed:  a.go, b.go", "result:   line one", "          line two"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}

func TestCgxc1PrintAgentAwaitTextNotesAnElapsedWaitBound(t *testing.T) {
	t.Parallel()
	result := agents.Result{AgentID: "agt-cgxc1", State: agents.StateRunning, Terminal: false}
	var out strings.Builder
	if err := printAgentAwaitText(&out, result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "note:     the wait bound elapsed") {
		t.Fatalf("missing note line: %s", out.String())
	}
}

func TestCgxc1PrintAgentAwaitTextPropagatesWriteErrorAtEveryStage(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stdout is closed")

	t.Run("changed line", func(t *testing.T) {
		t.Parallel()
		result := agents.Result{AgentID: "agt-cgxc1", State: agents.StateCompleted, Terminal: true, Changes: &agents.ChangeSummary{Files: []string{"a.go"}}}
		// printAgentStatusText writes 10 lines for this result (9 base lines
		// plus the "changes:" line, since Changes != nil); the "changed:"
		// line printAgentAwaitText itself writes is the 11th write.
		err := printAgentAwaitText(&cgxc1FailAfterWriter{after: 10, err: sentinel}, result)
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
	})

	t.Run("result line", func(t *testing.T) {
		t.Parallel()
		result := agents.Result{AgentID: "agt-cgxc1", State: agents.StateCompleted, Terminal: true, Result: "done"}
		// printAgentStatusText writes 9 lines for this result (no Changes);
		// the "result:" line is the 10th write.
		err := printAgentAwaitText(&cgxc1FailAfterWriter{after: 9, err: sentinel}, result)
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
	})

	t.Run("note line", func(t *testing.T) {
		t.Parallel()
		result := agents.Result{AgentID: "agt-cgxc1", State: agents.StateRunning, Terminal: false}
		// No Changes and no Result: the "note:" line is the 10th write.
		err := printAgentAwaitText(&cgxc1FailAfterWriter{after: 9, err: sentinel}, result)
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
	})
}

func TestCgxc1IndentContinuationIndentsWrappedLines(t *testing.T) {
	t.Parallel()
	got := indentContinuation("first\nsecond\n")
	want := "first\n          second"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCgxc1PrintAgentListPropagatesWriteError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stdout is closed")
	err := printAgentList(failingWriter{err: sentinel}, []agents.Result{{AgentID: "agt-cgxc1", State: agents.StateCompleted, StartedAt: time.Now()}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

// ----------------------------------------------------- remote nil-Result closures

func TestCgxc1RemoteDispatchWithNilResultIsAFinding(t *testing.T) {
	agentTestEnv(t)
	remoteConfigHome(t, exampleRemoteConfig)
	fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteDispatch,
	}))
	code, _, stderr := runAgentCLI(t, "agent", "dispatch", "--to", "hetzner-vm1",
		"--new-worktree", "remote-task", "--repo", "acme/app", "--profile", "cheap", "--task", "do it")
	if code != exitFindings || !strings.Contains(stderr, "returned no dispatch result") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestCgxc1RemoteStatusWithNilResultIsAFinding(t *testing.T) {
	agentTestEnv(t)
	remoteConfigHome(t, exampleRemoteConfig)
	fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteStatus,
	}))
	code, _, stderr := runAgentCLI(t, "agent", "status", "--to", "hetzner-vm1", "agt-00000000000000000000000000000000")
	if code != exitFindings || !strings.Contains(stderr, "returned no status result") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestCgxc1RemoteAwaitWithNilResultIsAFinding(t *testing.T) {
	agentTestEnv(t)
	remoteConfigHome(t, exampleRemoteConfig)
	fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteAwait,
	}))
	code, _, stderr := runAgentCLI(t, "agent", "await", "--to", "hetzner-vm1", "agt-00000000000000000000000000000000")
	if code != exitFindings || !strings.Contains(stderr, "returned no await result") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestCgxc1RemoteStopWithNilResultIsAFinding(t *testing.T) {
	agentTestEnv(t)
	remoteConfigHome(t, exampleRemoteConfig)
	fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteStop,
	}))
	code, _, stderr := runAgentCLI(t, "agent", "stop", "--to", "hetzner-vm1", "agt-00000000000000000000000000000000")
	if code != exitFindings || !strings.Contains(stderr, "returned no stop result") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestCgxc1RemoteListJSONReportsTheMachine(t *testing.T) {
	agentTestEnv(t)
	remoteConfigHome(t, exampleRemoteConfig)
	result := agents.Result{AgentID: "agt-cgxc1", State: agents.StateCompleted}
	fakeSSHOnPath(t, encodeRemoteResponseForTest(t, agents.RemoteResponse{
		SchemaVersion: 1, Operation: agents.RemoteList, Results: []agents.Result{result},
	}))
	code, stdout, stderr := runAgentCLI(t, "agent", "list", "--to", "hetzner-vm1", "--json")
	if code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "hetzner-vm1") || !strings.Contains(stdout, "agt-cgxc1") {
		t.Fatalf("stdout = %s", stdout)
	}
}
