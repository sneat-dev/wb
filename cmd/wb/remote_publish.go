package main

import (
	"io"

	"github.com/sneat-dev/wb/internal/cli/remotepublishview"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/remotepublish"
)

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
