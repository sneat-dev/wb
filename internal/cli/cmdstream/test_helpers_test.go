package cmdstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/streamrun"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }

const (
	exitOK       = 0
	exitFindings = 1
	exitUsage    = 2
)

func testRuntime() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "/fixture"} }, ExitError: func(code int, message string) error { return &exitError{code: code, message: message} }}
}
func exitCodeOfSafe(err error) int {
	if err == nil {
		return exitOK
	}
	var e *exitError
	if errors.As(err, &e) {
		return e.code
	}
	return exitFindings
}
func exitCodeOf(_ *testing.T, err error) int { return exitCodeOfSafe(err) }
func testDependencies() Dependencies {
	return Dependencies{
		Start: func(context.Context, streamrun.Creation, streams.StartOptions) (streams.StartResult, error) {
			return streams.StartResult{}, nil
		},
		Join: func(context.Context, streamrun.Creation, streams.JoinOptions) (streams.StartResult, error) {
			return streams.StartResult{}, nil
		},
		List: func(context.Context, string) ([]streams.Stream, []streams.Unreadable, error) { return nil, nil, nil },
		Status: func(context.Context, string, string) (streams.Status, error) {
			return streams.Status{}, streams.ErrNotFound
		},
		End: func(context.Context, string, streams.EndOptions) (streams.EndResult, error) {
			return streams.EndResult{}, nil
		},
		Delete: func(string, string) error { return streams.ErrNotFound },
		Sync: func(context.Context, streamrun.SyncRequest) ([]streamsync.Result, error) {
			return nil, streams.ErrNotFound
		},
		RegisteredSession: func() bool { return false }, PrepareWorkLog: func(_, _ string, opts worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
			if opts.Model == "" {
				return opts, errors.New("--model is required")
			}
			return opts, nil
		}}
}
func cwDepsNewOutCommand(out io.Writer) *cobra.Command {
	c := &cobra.Command{}
	c.SetOut(out)
	c.SetErr(out)
	return c
}

type cwDepsFailingWriter struct{}

func (cwDepsFailingWriter) Write([]byte) (int, error) { return 0, errors.New("cwDeps: write refused") }

type failAfterWriter struct{ allowedWrites, calls int }

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > w.allowedWrites {
		return 0, fmt.Errorf("zz_rwi03: forced write failure on call %d", w.calls)
	}
	return len(p), nil
}
func cwCovExec(_ *testing.T, _ string, build func() *cobra.Command, args ...string) (string, string, error) {
	c := build()
	var out, errOut bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errOut)
	c.SetArgs(args)
	err := c.Execute()
	return out.String(), errOut.String(), err
}
func executeFamily(args []string, out, errOut io.Writer) int {
	c := New(testRuntime(), testDependencies())
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetOut(out)
	c.SetErr(errOut)
	c.SetArgs(args[1:])
	err := c.Execute()
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
	}
	return exitCodeOfSafe(err)
}
func noOperationDependencies(t *testing.T) Dependencies {
	t.Helper()
	return Dependencies{
		Start: func(context.Context, streamrun.Creation, streams.StartOptions) (streams.StartResult, error) {
			t.Fatal("invalid input invoked Start")
			return streams.StartResult{}, nil
		},
		Join: func(context.Context, streamrun.Creation, streams.JoinOptions) (streams.StartResult, error) {
			t.Fatal("invalid input invoked Join")
			return streams.StartResult{}, nil
		},
		List: func(context.Context, string) ([]streams.Stream, []streams.Unreadable, error) {
			t.Fatal("invalid input invoked List")
			return nil, nil, nil
		},
		Status: func(context.Context, string, string) (streams.Status, error) {
			t.Fatal("invalid input invoked Status")
			return streams.Status{}, nil
		},
		End: func(context.Context, string, streams.EndOptions) (streams.EndResult, error) {
			t.Fatal("invalid input invoked End")
			return streams.EndResult{}, nil
		},
		Delete: func(string, string) error { t.Fatal("invalid input invoked Delete"); return nil },
		Sync: func(context.Context, streamrun.SyncRequest) ([]streamsync.Result, error) {
			t.Fatal("invalid input invoked Sync")
			return nil, nil
		},
		RegisteredSession: func() bool { t.Fatal("invalid input read registration"); return false },
		PrepareWorkLog: func(string, string, worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
			t.Fatal("invalid input prepared provenance")
			return worktrees.WorkLogOptions{}, nil
		}}
}
