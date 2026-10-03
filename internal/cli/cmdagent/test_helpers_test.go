package cmdagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
	"io"
	"testing"
	"time"
)

const (
	exitOK       = 0
	exitFindings = 1
	exitUsage    = 2
)

type codedError struct {
	code    int
	message string
}

func (e *codedError) Error() string { return e.message }
func testRuntime() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "/fixture"} }, ExitError: func(code int, msg string) error { return &codedError{code, msg} }}
}
func fakeDependencies() Dependencies {
	return Dependencies{Operations: agentrun.Operations{}, ResolveRemote: func(machine string) (agents.RemoteTarget, error) { return agents.RemoteTarget{Machine: machine}, nil }, CallRemote: func(context.Context, agents.RemoteTarget, agents.RemoteRequest) (agents.RemoteResponse, error) {
		return agents.RemoteResponse{}, nil
	}, ReadTaskFile: func(string) ([]byte, error) { return nil, errors.New("missing file") }, Discovery: func(*cobra.Command, string) {}}
}
func runFake(t *testing.T, deps Dependencies, args ...string) (int, string, string) {
	t.Helper()
	var out, errout bytes.Buffer
	cmd := New(testRuntime(), deps)
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &codedError{2, err.Error()} })
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(&out)
	cmd.SetErr(&errout)
	cmd.SetArgs(args[1:])
	err := cmd.ExecuteContext(t.Context())
	code := 0
	if err != nil {
		code = 1
		var coded *codedError
		if errors.As(err, &coded) {
			code = coded.code
		}
		fmt.Fprintln(&errout, err)
	}
	return code, out.String(), errout.String()
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func fakeCommandRoot(deps Dependencies) *cobra.Command {
	cmd := &cobra.Command{Use: "wb", SilenceUsage: true, SilenceErrors: true}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &codedError{2, err.Error()} })
	cmd.AddCommand(New(testRuntime(), deps))
	return cmd
}
func runFakeWithStdin(t *testing.T, deps Dependencies, args []string, in io.Reader, out, errout io.Writer) int {
	t.Helper()
	cmd := fakeCommandRoot(deps)
	cmd.SetArgs(args)
	cmd.SetIn(in)
	cmd.SetOut(out)
	cmd.SetErr(errout)
	err := cmd.Execute()
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintln(errout, err)
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	return 1
}
func presentationRecord(mutate func(*agents.Record)) agents.Record {
	record := agents.Record{AgentID: "agt-00000000000000000000000000000001", State: agents.StateRunning, RequestedProfile: "cheap", Resolved: agents.Resolved{Profile: "cheap", Harness: agents.HarnessCodex, Provider: "deepseek", Model: "deepseek-flash", Reasoning: "high"}, Task: "the private bounded task", TaskSummary: "the private bounded task", Repository: "acme/app", WorktreeMode: agents.ModeNew, Worktree: "task-one", WorktreeDir: "/fleet/.worktrees/task-one", Branch: "task-one", Base: "main", BaseSHA: "abc123", StartedAt: time.Unix(100, 0).UTC(), LogPath: "/private/run/events.jsonl"}
	mutate(&record)
	return record
}
