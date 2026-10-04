package main

import (
	"io"
	"os"

	"github.com/sneat-dev/wb/internal/cli/remotepublishview"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/spf13/cobra"
)

func newRemotePublishCmd(inv *invocation) *cobra.Command {
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
			return runRemotePublishWithProgress(defaultRemoteDeps(), inv.projectsRoot, inv.filterFlag, parallel, dryRun, jsonOut, os.Stdout, cmd.ErrOrStderr(), inv)
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "print the snapshot; publish nothing")
	addJSONFormatFlags(cmd, &jsonOut)
	cmd.Flags().IntVar(&parallel, "parallel", 8, "max concurrent repository scans")
	return cmd
}

func remotePublishDependencies(deps remoteDeps) remotepublish.Dependencies {
	result := remotepublish.DefaultDependencies(deps.configPath, newCLIErrorRuntime().ExitError)
	result.Login, result.Open, result.Now = deps.login, deps.open, deps.now

	return result
}
func runRemotePublishWithProgress(deps remoteDeps, projectsRoot, filter string, parallel int, dryRun, jsonOut bool, out, progressOut io.Writer, inv *invocation) error {
	progress := remotepublishview.NewProgress(progressOut, console.Interactive(progressOut, inv.nonInteractive))
	notes := progressOut
	if notes == nil {
		notes = deps.stderr
	}
	result, err := remotepublish.New(remotePublishDependencies(deps)).Publish(remotepublish.Request{ProjectsRoot: projectsRoot, Filter: filter, Parallel: parallel, DryRun: dryRun}, progress.Callbacks(), notes)
	if err != nil {
		return err
	}
	return remotepublishview.Write(out, result, jsonOut)
}
