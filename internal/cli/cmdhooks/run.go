package cmdhooks

import (
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/spf13/cobra"
)

func (family commands) newHooksRunCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:    "run <hook> [hook-args...]",
		Short:  "Run a configured hook template (normally invoked by a managed shim)",
		Args:   cobra.MinimumNArgs(1),
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := family.git.Run(hooks.RunOptions{
				RepoPath:     ".",
				ConfigPath:   configPath,
				Hook:         args[0],
				Args:         args[1:],
				Stdin:        cmd.InOrStdin(),
				Stdout:       cmd.OutOrStdout(),
				Stderr:       cmd.ErrOrStderr(),
				WBExecutable: family.git.Executable(),
				ProjectsRoot: family.runtime.Flags().ProjectsRoot,
			})
			if result.MetricsError != nil {
				// A metrics warning must never turn a successful Git hook into a
				// failure, including when stderr itself is unavailable.
				_ = shared.WriteLine(cmd.ErrOrStderr(), "warning: hook succeeded but local metrics could not be recorded:", result.MetricsError)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "explicit hooks policy")
	return cmd
}
