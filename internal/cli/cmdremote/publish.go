package cmdremote

import (
	"github.com/sneat-dev/wb/internal/cli/remotepublishview"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/spf13/cobra"
)

func newPublish(runtime shared.Runtime, operations Operations) *cobra.Command {
	var dryRun, jsonOut bool
	var parallel int
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Scan this machine's fleet and publish the snapshot to the remote store",
		Long: `Scans every clone under --projects-root (honouring --filter), lists live
task worktrees, and publishes one snapshot keyed <login>/<machine>, with this
machine's os, arch, cpu_count and boot_time (the first publish after an upgrade
says so once). Agents and metrics are never published by hand: only the daemon's
periodic publish, which remote.publish.interval turns on, carries them, each
behind remote.publish.agents and remote.publish.metrics.
--dry-run prints the snapshot and writes nothing, locally or remotely.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			progress := remotepublishview.NewProgress(cmd.ErrOrStderr(), console.Interactive(cmd.ErrOrStderr(), runtime.Flags().NonInteractive))
			result, err := operations.Publish(remotepublish.Request{ProjectsRoot: runtime.Flags().ProjectsRoot, Filter: runtime.Flags().Filter, Parallel: parallel, DryRun: dryRun}, progress.Callbacks(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return remotepublishview.Write(cmd.OutOrStdout(), result, jsonOut)
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the snapshot; publish nothing")
	shared.AddJSONFormatFlags(cmd, &jsonOut)
	cmd.Flags().IntVar(&parallel, "parallel", 8, "max concurrent repository scans")
	return cmd
}
