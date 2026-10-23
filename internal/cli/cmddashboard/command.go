package cmddashboard

import (
	"context"
	"fmt"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

type Operations struct {
	Open     func(string) error
	LocalURL func(context.Context, string) (address, warning string, err error)
}

func New(runtime shared.Runtime, operations Operations) *cobra.Command {
	var local, jsonOut bool
	var format string
	command := &cobra.Command{
		Use:   "dashboard",
		Short: "Open the hosted cross-machine Workbench dashboard",
		Long: "Open the hosted cross-machine Workbench dashboard. This machine's own web interface is Cockpit: " +
			"run `wb cockpit`. The local operations pages that --local, --metrics and --coverage used to open " +
			"are retired; --local now opens Cockpit and is deprecated in favour of `wb cockpit`.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			target, scope := hostedDashboardURL, "hosted"
			if local {
				_, _ = fmt.Fprintln(command.ErrOrStderr(), "wb:", dashboardLocalDeprecation)
				var warning string
				target, warning, err = operations.LocalURL(command.Context(), runtime.Flags().ProjectsRoot)
				if err != nil {
					return err
				}
				if warning != "" {
					_, _ = fmt.Fprintln(command.ErrOrStderr(), "wb:", warning)
				}
				if target, err = localCockpitURL(target); err != nil {
					return err
				}
				scope = "local"
			}
			result := dashboardOpenResult{URL: target, Scope: scope}
			if format == "text" && !runtime.Flags().NonInteractive {
				if err := operations.Open(target); err != nil {
					return fmt.Errorf("open %s dashboard %s: %w", scope, target, err)
				}
				result.Opened = true
			}
			return writeDashboardOpenResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().BoolVar(&local, "local", false, "deprecated: open this machine's Cockpit (use `wb cockpit`)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}
