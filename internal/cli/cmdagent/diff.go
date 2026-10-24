package cmdagent

import (
	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

// NewDiff builds `wb agent diff`.
func NewDiff(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var patch, jsonOut bool
	command := &cobra.Command{
		Use:   "diff <agent-id>",
		Short: "Show what a finished execution changed in its worktree",
		Long: `Show the tracked change an execution left behind, measured from the base it
started at.

The default is a diffstat plus the untracked paths Git leaves out of a patch.
Pass --patch for the patch itself, bounded so a huge change cannot flood a
caller's context. The patch is not a review: WB never judges whether the change
is correct.`,
		Example: `wb agent diff agt-0123456789abcdef0123456789abcdef
wb agent diff agt-0123456789abcdef0123456789abcdef --patch`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			document, err := deps.Diff(command.Context(), agentrun.DiffRequest{
				ProjectsRoot: runtime.Flags().ProjectsRoot, AgentID: args[0], Patch: patch,
			})
			if err != nil {
				if agents.IsUnknownAgent(err) {
					return runtime.ExitError(shared.ExitFindings, err.Error())
				}
				return agentRequestError(runtime, err)
			}
			if jsonOut {
				return writeAgentJSON(command.OutOrStdout(), struct {
					AgentID string `json:"agent_id"`
					Diff    string `json:"diff"`
					Patch   bool   `json:"patch"`
				}{AgentID: args[0], Diff: document, Patch: patch})
			}
			return writeText(command.OutOrStdout(), document)
		},
	}
	deps.Discovery(command, "diff patch stat changed files review inspect worker")
	command.Flags().BoolVar(&patch, "patch", false, "print the patch itself instead of a diffstat")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}
