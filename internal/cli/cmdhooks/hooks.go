package cmdhooks

import (
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func (family commands) newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Install, validate, run, and measure user-owned Git hooks",
	}
	cmd.AddCommand(family.newHooksInstallCmd(false))
	cmd.AddCommand(family.newHooksCheckCmd())
	cmd.AddCommand(family.newHooksInstallCmd(true))
	cmd.AddCommand(family.newHooksRunCmd())
	cmd.AddCommand(family.newHooksAgentCmd())
	cmd.AddCommand(family.newHooksMetricsCmd())
	cmd.AddCommand(family.newHooksMeasureCmd())
	cmd.AddCommand(family.newHooksPushTierCmd())
	cmd.AddCommand(family.newHooksLifecycleCmd())
	return cmd
}

// New registers the complete hooks family with per-command operations.
func New(runtime shared.Runtime, git GitOperations, life LifecycleOperations, agent AgentOperations) *cobra.Command {
	return (commands{runtime: runtime, git: git, life: life, agent: agent}).newHooksCmd()
}

type commands struct {
	runtime shared.Runtime
	git     GitOperations
	life    LifecycleOperations
	agent   AgentOperations
}
