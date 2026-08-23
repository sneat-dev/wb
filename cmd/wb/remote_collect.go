package main

import (
	"context"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// collectSnapshot scans the local fleet the way wb fleet status does and
// lists live task worktrees, then assembles the snapshot to publish.
func collectSnapshot(projectsRoot, filter string, parallel int, identity remotestate.Snapshot, redaction remotestate.Redaction) (remotestate.Snapshot, error) {
	targets, err := qualityTargets("", projectsRoot, filter, qualityOptions{fleet: true, parallel: parallel})
	if err != nil {
		return remotestate.Snapshot{}, err
	}
	inputs := make([]remotestate.RepositoryInput, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		target := targets[index]
		input := remotestate.RepositoryInput{Repository: target.repository, Path: target.path}
		if input.Status, input.Err = gitops.Status(target.path); input.Err != nil {
			inputs[index] = input
			return
		}
		input.Tracking, input.Err = gitops.Tracking(target.path)
		inputs[index] = input
	})
	wts, err := worktrees.List(context.Background(), worktrees.ListOptions{ProjectsRoot: projectsRoot, Filter: filter, OwnerState: "active"})
	if err != nil {
		return remotestate.Snapshot{}, err
	}
	identity.ProjectsRoot = projectsRoot
	return remotestate.Build(identity, inputs, wts, redaction), nil
}
