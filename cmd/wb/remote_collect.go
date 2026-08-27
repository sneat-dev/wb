package main

import (
	"context"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// collectSnapshot scans the local fleet the way wb fleet status does and
// lists live task worktrees, then assembles the snapshot to publish.
func collectSnapshot(projectsRoot, filter string, parallel int, identity remotestate.Snapshot, redaction remotestate.Redaction, progress *remotePublishProgress) (remotestate.Snapshot, error) {
	targets, err := qualityTargets("", projectsRoot, filter, qualityOptions{fleet: true, parallel: parallel, allowEmpty: filter == ""})
	if err != nil {
		return remotestate.Snapshot{}, err
	}
	progress.start(len(targets))
	inputs := make([]remotestate.RepositoryInput, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		target := targets[index]
		input := remotestate.RepositoryInput{Repository: target.repository, Path: target.path}
		if input.Status, input.Err = gitops.Status(target.path); input.Err != nil {
			inputs[index] = input
			progress.repositoryComplete(target.repository, input.Err)
			return
		}
		input.Tracking, input.Err = gitops.Tracking(target.path)
		inputs[index] = input
		progress.repositoryComplete(target.repository, input.Err)
	})
	// No OwnerState filter: this snapshot is a fleet-audit artifact, and
	// abandoned worktrees (sessions that exited without cleanup) are exactly
	// what cross-machine reconciliation needs to see. Filtering to "active"
	// here made `wb remote publish` under-report worktree counts on any
	// machine holding orphaned worktrees.
	progress.phase("inspecting worktrees")
	wts, err := worktrees.List(context.Background(), worktrees.ListOptions{
		ProjectsRoot: projectsRoot,
		Filter:       filter,
		Progress:     progress.worktree,
	})
	if err != nil {
		return remotestate.Snapshot{}, err
	}
	identity.ProjectsRoot = projectsRoot
	return remotestate.Build(identity, inputs, wts, redaction), nil
}
