package main

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/sneat-dev/wb/internal/cli/cmdpr"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/landingcontext"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/spf13/cobra"
)

func newPRCmd(inv *invocation) *cobra.Command {
	return cmdpr.New(newCLIRuntime(inv), prDependencies())
}

func prDependencies() cmdpr.Dependencies {
	return cmdpr.Dependencies{Create: orchestrate.CreatePullRequest, Update: orchestrate.UpdatePullRequest, Land: orchestrate.LandPullRequest,
		CheckRepository: func(root, repository string) error {
			return landingGuardError(landingcontext.CheckRepository(root, repository))
		}, Events: landingcontext.Events,
		Lane: func(root, command, reason string, takeOver bool) orchestrate.LaneGuardRequest {
			return landingcontext.LaneRequest(root, command, reason, takeOver, os.Getpid())
		},
		CheckoutUpdated: func(out io.Writer) func(context.Context, orchestrate.CheckoutUpdate) {
			return landingcontext.CheckoutUpdated(out, lifecyclehooks.Dispatch)
		}, SuggestCloses: landingcontext.SuggestCloses,
		Interactive: func(out io.Writer, nonInteractive bool) bool { return console.Interactive(out, nonInteractive) }, IsTerminal: func(out io.Writer) bool { return console.IsTerminal(out) }}
}
func landingGuardError(err error) error {
	var refusal *landingcontext.Refusal
	if errors.As(err, &refusal) {
		return &exitError{code: exitUsage, message: refusal.Message}
	}
	return err
}
