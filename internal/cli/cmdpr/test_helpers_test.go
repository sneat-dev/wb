package cmdpr

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/spf13/cobra"
)

const (
	exitUsage            = 2
	exitFindings         = 1
	exitLandedIncomplete = 3
)

type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }
func testRuntime() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "/projects"} }, ExitError: func(code int, message string) error { return &exitError{code, message} }}
}
func testDependencies() Dependencies {
	return Dependencies{
		Create: func(context.Context, orchestrate.PullRequestCreateOptions) (orchestrate.PullRequestCreateResult, error) {
			return orchestrate.PullRequestCreateResult{Outcome: orchestrate.CreateSuccess}, nil
		},
		Update: func(context.Context, orchestrate.PullRequestUpdateOptions) (orchestrate.PullRequestUpdateResult, error) {
			return orchestrate.PullRequestUpdateResult{}, nil
		},
		Land: func(context.Context, orchestrate.PullRequestLandOptions) (orchestrate.PullRequestLandResult, error) {
			return orchestrate.PullRequestLandResult{Outcome: orchestrate.LandSuccess}, nil
		},
		CheckRepository: func(string, string) error { return nil }, Events: func(string, string) (streams.EventAppender, string) { return streams.DiscardEvents{}, "" }, Lane: func(string, string, string, bool) orchestrate.LaneGuardRequest { return orchestrate.LaneGuardRequest{} },
		CheckoutUpdated: func(io.Writer) func(context.Context, orchestrate.CheckoutUpdate) {
			return func(context.Context, orchestrate.CheckoutUpdate) {}
		}, SuggestCloses: func(context.Context, string, string) []int { return nil }, Interactive: func(io.Writer, bool) bool { return false }, IsTerminal: func(io.Writer) bool { return false }}
}
func outputCommand(out io.Writer) *cobra.Command { c := &cobra.Command{}; c.SetOut(out); return c }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func executeTest(c *cobra.Command, args []string, out, errOut io.Writer) int {
	c.SetOut(out)
	c.SetErr(errOut)
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetArgs(args)
	err := c.Execute()
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintln(errOut, err)
	var coded *exitError
	if errors.As(err, &coded) {
		return coded.code
	}
	return 1
}
