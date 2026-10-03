package cmdrun

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/runexec"
	"testing"
)

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
func TestChangedNoWorkWriterErrorStopsExecution(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("write refused")
	deps := fakeDependencies()
	deps.Changed = func(context.Context, runexec.ChangedRequest) (runexec.ChangedResult, error) {
		return runexec.ChangedResult{NoWork: true, Target: "main", MergeBase: "abc"}, nil
	}
	deps.Execute = func(context.Context, runexec.ExecuteRequest) (runexec.ExecuteResult, error) {
		t.Fatal("must not run no-work command")
		return runexec.ExecuteResult{}, nil
	}
	command := New(testRuntime(), deps)
	command.SetOut(failingWriter{sentinel})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--changed", "--target", "main", "--", "go", "test"})
	command.SilenceErrors = true
	command.SilenceUsage = true
	if err := command.Execute(); err != sentinel {
		t.Fatalf("error=%v", err)
	}
}
