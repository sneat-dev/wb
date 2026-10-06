package cmdrun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/operationreceipt"
	"github.com/sneat-dev/wb/internal/runexec"
	"github.com/sneat-dev/wb/internal/runqueue"
	"io"
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
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }, ExitError: func(code int, message string) error { return &codedError{code, message} }}
}
func fakeDependencies() Dependencies {
	return Dependencies{
		Execute: func(context.Context, runexec.ExecuteRequest) (runexec.ExecuteResult, error) {
			return runexec.ExecuteResult{}, nil
		},
		Changed: func(context.Context, runexec.ChangedRequest) (runexec.ChangedResult, error) {
			return runexec.ChangedResult{NoWork: true}, nil
		},
		History: func(context.Context, runexec.HistoryRequest) (runexec.HistoryResult, error) {
			return runexec.HistoryResult{}, nil
		},
		Queue: func(runexec.QueueRequest) runqueue.QueueListing { return runqueue.QueueListing{} },
		Recipes: func(context.Context, runexec.RecipeRequest) (runexec.RecipeResult, error) {
			return runexec.RecipeResult{}, nil
		},
		Submit: func(context.Context, runexec.SubmitRequest) (operationreceipt.Receipt, error) {
			return operationreceipt.Receipt{}, nil
		},
		LoadHint: func(string) string { return "capacity contention" },
	}
}
func run(args []string, out, errOut io.Writer) int {
	command := New(testRuntime(), fakeDependencies())
	command.SetOut(out)
	command.SetErr(errOut)
	command.SetArgs(args[1:])
	command.SilenceUsage = true
	command.SilenceErrors = true
	err := command.Execute()
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintln(errOut, err)
	if coded, ok := err.(*codedError); ok {
		return coded.code
	}
	return 1
}
