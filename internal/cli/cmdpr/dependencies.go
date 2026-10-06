package cmdpr

import (
	"context"
	"io"
	"strings"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/spf13/cobra"
)

type Dependencies struct {
	Create          func(context.Context, orchestrate.PullRequestCreateOptions) (orchestrate.PullRequestCreateResult, error)
	Update          func(context.Context, orchestrate.PullRequestUpdateOptions) (orchestrate.PullRequestUpdateResult, error)
	Land            func(context.Context, orchestrate.PullRequestLandOptions) (orchestrate.PullRequestLandResult, error)
	CheckRepository func(string, string) error
	Events          func(string, string) (streams.EventAppender, string)
	Lane            func(string, string, string, bool) orchestrate.LaneGuardRequest
	CheckoutUpdated func(io.Writer) func(context.Context, orchestrate.CheckoutUpdate)
	SuggestCloses   func(context.Context, string, string) []int
	Interactive     func(io.Writer, bool) bool
	IsTerminal      func(io.Writer) bool
}

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "pr", Short: "Update or land pull requests with exact-head evidence"}
	command.AddCommand(NewCreate(runtime, deps), NewUpdate(runtime, deps), NewLand(runtime, deps))
	return command
}
func landingProgress(runtime shared.Runtime, deps Dependencies, command *cobra.Command, nonInteractive bool) *cliprogress.Checks {
	interactive := deps.Interactive(command.ErrOrStderr(), nonInteractive)
	return cliprogress.NewChecks(cliprogress.Output(command.ErrOrStderr(), interactive), !runtime.Flags().Quiet)
}
func suggestedCloses(runtime shared.Runtime, deps Dependencies, ctx context.Context, arg string) []int {
	if runtime.Flags().Quiet {
		return nil
	}
	return deps.SuggestCloses(ctx, runtime.Flags().ProjectsRoot, arg)
}
func discovery(command *cobra.Command, terms string) {
	if command.Annotations == nil {
		command.Annotations = map[string]string{}
	}
	command.Annotations["wb.dev/discovery-terms"] = terms
}
func quietVerb(command *cobra.Command) {
	discovery(command, strings.TrimSpace(command.Annotations["wb.dev/discovery-terms"]+" quiet outcome only no progress pipe tail"))
}
func landingGuard(command *cobra.Command) *cobra.Command {
	command.Annotations["wb.dev/landing-guard"] = "pull-request"
	return command
}
