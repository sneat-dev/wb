package cmdagent

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/spf13/cobra"
	"strings"
	"testing"
	"time"
)

func worktreeModeCommand() *cobra.Command {
	command := &cobra.Command{}
	command.Flags().String("new-worktree", "", "")
	command.Flags().String("use-worktree", "", "")
	return command
}

type failAfterWriter struct {
	after int
	err   error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.after <= 0 {
		return 0, w.err
	}
	w.after--
	return len(p), nil
}

func TestSelectWorktreeModeCoversEveryBranch(t *testing.T) {
	t.Parallel()

	t.Run("both flags explicitly changed is a conflict", func(t *testing.T) {
		t.Parallel()
		command := worktreeModeCommand()
		if err := command.Flags().Set("new-worktree", "a"); err != nil {
			t.Fatal(err)
		}
		if err := command.Flags().Set("use-worktree", "b"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := selectWorktreeMode(testRuntime(), fakeDependencies(), command, "a", "b"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("both values non-empty without Changed is still a conflict", func(t *testing.T) {
		t.Parallel()
		command := worktreeModeCommand()
		if _, _, err := selectWorktreeMode(testRuntime(), fakeDependencies(), command, "a", "b"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("new only", func(t *testing.T) {
		t.Parallel()
		command := worktreeModeCommand()
		mode, name, err := selectWorktreeMode(testRuntime(), fakeDependencies(), command, "created-name", "")
		if err != nil || mode != agents.ModeNew || name != "created-name" {
			t.Fatalf("mode=%q name=%q err=%v", mode, name, err)
		}
	})

	t.Run("existing only", func(t *testing.T) {
		t.Parallel()
		command := worktreeModeCommand()
		mode, name, err := selectWorktreeMode(testRuntime(), fakeDependencies(), command, "", "existing-name")
		if err != nil || mode != agents.ModeExisting || name != "existing-name" {
			t.Fatalf("mode=%q name=%q err=%v", mode, name, err)
		}
	})

	t.Run("neither is required", func(t *testing.T) {
		t.Parallel()
		command := worktreeModeCommand()
		if _, _, err := selectWorktreeMode(testRuntime(), fakeDependencies(), command, "", ""); err == nil || !strings.Contains(err.Error(), "exactly one of") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestReadAgentTaskCoversTaskFileBranches(t *testing.T) {
	t.Parallel()

	t.Run("task and task-file together is a usage error", func(t *testing.T) {
		t.Parallel()
		command := &cobra.Command{}
		if _, err := readAgentTask(testRuntime(), fakeDependencies(), command, "inline", "/some/path"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("task-file - reads stdin", func(t *testing.T) {
		t.Parallel()
		command := &cobra.Command{}
		command.SetIn(strings.NewReader("do the thing\n"))
		text, err := readAgentTask(testRuntime(), fakeDependencies(), command, "", "-")
		if err != nil || text != "do the thing\n" {
			t.Fatalf("text=%q err=%v", text, err)
		}
	})

	t.Run("task-file - refuses empty stdin", func(t *testing.T) {
		t.Parallel()
		command := &cobra.Command{}
		command.SetIn(strings.NewReader("   \n"))
		if _, err := readAgentTask(testRuntime(), fakeDependencies(), command, "", "-"); err == nil || !strings.Contains(err.Error(), "non-empty task on stdin") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("neither task nor task-file is required", func(t *testing.T) {
		t.Parallel()
		command := &cobra.Command{}
		if _, err := readAgentTask(testRuntime(), fakeDependencies(), command, "", ""); err == nil || !strings.Contains(err.Error(), "one of --task or --task-file is required") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRenderAgentResultWritesJSONWhenRequested(t *testing.T) {
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

func TestPrintAgentStatusTextCoversFailureAndWriteError(t *testing.T) {
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

func TestFinishAgentAwaitPropagatesJSONWriteError(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	sentinel := errors.New("stdout is closed")
	command.SetOut(failingWriter{err: sentinel})
	err := finishAgentAwait(testRuntime(), fakeDependencies(), command, true, agents.Result{AgentID: "agt-cgxc1", State: agents.StateCompleted, Terminal: true})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

func TestPrintAgentAwaitTextHappyPathCoversChangesAndResultLines(t *testing.T) {
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

func TestPrintAgentAwaitTextNotesAnElapsedWaitBound(t *testing.T) {
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

func TestPrintAgentAwaitTextPropagatesWriteErrorAtEveryStage(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stdout is closed")

	t.Run("changed line", func(t *testing.T) {
		t.Parallel()
		result := agents.Result{AgentID: "agt-cgxc1", State: agents.StateCompleted, Terminal: true, Changes: &agents.ChangeSummary{Files: []string{"a.go"}}}
		// printAgentStatusText writes 10 lines for this result (9 base lines
		// plus the "changes:" line, since Changes != nil); the "changed:"
		// line printAgentAwaitText itself writes is the 11th write.
		err := printAgentAwaitText(&failAfterWriter{after: 10, err: sentinel}, result)
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
	})

	t.Run("result line", func(t *testing.T) {
		t.Parallel()
		result := agents.Result{AgentID: "agt-cgxc1", State: agents.StateCompleted, Terminal: true, Result: "done"}
		// printAgentStatusText writes 9 lines for this result (no Changes);
		// the "result:" line is the 10th write.
		err := printAgentAwaitText(&failAfterWriter{after: 9, err: sentinel}, result)
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
	})

	t.Run("note line", func(t *testing.T) {
		t.Parallel()
		result := agents.Result{AgentID: "agt-cgxc1", State: agents.StateRunning, Terminal: false}
		// No Changes and no Result: the "note:" line is the 10th write.
		err := printAgentAwaitText(&failAfterWriter{after: 9, err: sentinel}, result)
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
	})
}

func TestIndentContinuationIndentsWrappedLines(t *testing.T) {
	t.Parallel()
	got := indentContinuation("first\nsecond\n")
	want := "first\n          second"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPrintAgentListPropagatesWriteError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("stdout is closed")
	err := printAgentList(failingWriter{err: sentinel}, []agents.Result{{AgentID: "agt-cgxc1", State: agents.StateCompleted, StartedAt: time.Now()}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

func TestRemoteDispatchWithNilResultIsAFinding(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.CallRemote = func(context.Context, agents.RemoteTarget, agents.RemoteRequest) (agents.RemoteResponse, error) {
		return agents.RemoteResponse{
			SchemaVersion: 1, Operation: agents.RemoteDispatch,
		}, nil
	}
	code, _, stderr := runFake(t, deps, "agent", "dispatch", "--to", "hetzner-vm1",
		"--new-worktree", "remote-task", "--repo", "acme/app", "--profile", "cheap", "--task", "do it")
	if code != exitFindings || !strings.Contains(stderr, "returned no dispatch result") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestRemoteStatusWithNilResultIsAFinding(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.CallRemote = func(context.Context, agents.RemoteTarget, agents.RemoteRequest) (agents.RemoteResponse, error) {
		return agents.RemoteResponse{
			SchemaVersion: 1, Operation: agents.RemoteStatus,
		}, nil
	}
	code, _, stderr := runFake(t, deps, "agent", "status", "--to", "hetzner-vm1", "agt-00000000000000000000000000000000")
	if code != exitFindings || !strings.Contains(stderr, "returned no status result") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestRemoteAwaitWithNilResultIsAFinding(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.CallRemote = func(context.Context, agents.RemoteTarget, agents.RemoteRequest) (agents.RemoteResponse, error) {
		return agents.RemoteResponse{
			SchemaVersion: 1, Operation: agents.RemoteAwait,
		}, nil
	}
	code, _, stderr := runFake(t, deps, "agent", "await", "--to", "hetzner-vm1", "agt-00000000000000000000000000000000")
	if code != exitFindings || !strings.Contains(stderr, "returned no await result") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestRemoteStopWithNilResultIsAFinding(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.CallRemote = func(context.Context, agents.RemoteTarget, agents.RemoteRequest) (agents.RemoteResponse, error) {
		return agents.RemoteResponse{
			SchemaVersion: 1, Operation: agents.RemoteStop,
		}, nil
	}
	code, _, stderr := runFake(t, deps, "agent", "stop", "--to", "hetzner-vm1", "agt-00000000000000000000000000000000")
	if code != exitFindings || !strings.Contains(stderr, "returned no stop result") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestRemoteListJSONReportsTheMachine(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	result := agents.Result{AgentID: "agt-cgxc1", State: agents.StateCompleted}
	deps.CallRemote = func(context.Context, agents.RemoteTarget, agents.RemoteRequest) (agents.RemoteResponse, error) {
		return agents.RemoteResponse{
			SchemaVersion: 1, Operation: agents.RemoteList, Results: []agents.Result{result},
		}, nil
	}
	code, stdout, stderr := runFake(t, deps, "agent", "list", "--to", "hetzner-vm1", "--json")
	if code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "hetzner-vm1") || !strings.Contains(stdout, "agt-cgxc1") {
		t.Fatalf("stdout = %s", stdout)
	}
}
