package main

import (
	"context"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// readRepositoryWithGit reads one repository's status and tracking with Git: it
// is what a scan costs, several Git processes for each repository.
func readRepositoryWithGit(path string) (gitops.RepoStatus, gitops.TrackingState, error) {
	status, err := gitops.Status(path)
	if err != nil {
		return status, gitops.TrackingState{}, err
	}
	tracking, err := gitops.Tracking(path)
	return status, tracking, err
}

// repositoryScans reads the repositories of a scan, and keeps what it read of
// each clone for as long as the clone's change fingerprint stays the same, so a
// scan made because one repository changed reads that one with Git and not the
// whole fleet again. It is the daemon's periodic publisher's; a nil
// *repositoryScans reads every repository with Git every time, which is what
// `wb remote publish` by hand does.
//
// What the fingerprint does not see (a new untracked file, an unstaged edit of a
// tracked file: cockpitfleet.Fingerprint) is read again when the clone's
// fingerprint next moves, or when what is kept of it is as old as maxAge, which
// is the publisher's keepalive: nothing older than that is ever published as
// what a repository is like. A read that failed is never kept, and neither is
// one of a clone whose fingerprint could not be computed.
type repositoryScans struct {
	read        func(path string) (gitops.RepoStatus, gitops.TrackingState, error)
	fingerprint func(path string) (string, error)
	maxAge      time.Duration

	mu   sync.Mutex
	kept map[string]keptScan
}

// keptScan is what one read of a repository found, the fingerprint the clone
// had just before it and when it was made.
type keptScan struct {
	fingerprint string
	at          time.Time
	status      gitops.RepoStatus
	tracking    gitops.TrackingState
}

// of is the status and tracking of the repository at path as of now: what is
// kept of it when its fingerprint has not moved, and a read with Git otherwise.
// The fingerprint is taken before the read, so a change made while Git reads is
// a different fingerprint at the next scan.
func (s *repositoryScans) of(path string, now time.Time) (gitops.RepoStatus, gitops.TrackingState, error) {
	if s == nil {
		return readRepositoryWithGit(path)
	}
	fingerprint, err := s.fingerprint(path)
	if err != nil {
		fingerprint = ""
	}
	s.mu.Lock()
	kept, found := s.kept[path]
	s.mu.Unlock()
	if found && fingerprint != "" && kept.fingerprint == fingerprint && now.Sub(kept.at) < s.maxAge {
		return kept.status, kept.tracking, nil
	}
	status, tracking, err := s.read(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil || fingerprint == "" {
		delete(s.kept, path)
		return status, tracking, err
	}
	if s.kept == nil {
		s.kept = map[string]keptScan{}
	}
	s.kept[path] = keptScan{fingerprint: fingerprint, at: now, status: status, tracking: tracking}
	return status, tracking, nil
}

// keepOnly forgets every repository that is not among paths: one that left the
// fleet is not kept for ever.
func (s *repositoryScans) keepOnly(paths map[string]bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for path := range s.kept {
		if !paths[path] {
			delete(s.kept, path)
		}
	}
}

// collectSnapshot scans the local fleet the way wb fleet status does and
// lists live task worktrees, then assembles the snapshot to publish. A ctx that
// ends stops the scan between repositories and returns its error: the daemon's
// periodic publish bounds one attempt with it. scans is how each repository is
// read: nil reads every one with Git, as a publish by hand does.
func collectSnapshot(ctx context.Context, projectsRoot, filter string, parallel int, identity remotestate.Snapshot, redaction remotestate.Redaction, progress *remotePublishProgress, scans *repositoryScans) (remotestate.Snapshot, error) {
	targets, err := qualityTargets("", projectsRoot, filter, qualityOptions{fleet: true, parallel: parallel, allowEmpty: filter == ""})
	if err != nil {
		return remotestate.Snapshot{}, err
	}
	progress.start(len(targets))
	inputs := make([]remotestate.RepositoryInput, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		target := targets[index]
		if ctx.Err() != nil {
			inputs[index] = remotestate.RepositoryInput{Repository: target.repository, Path: target.path, Err: ctx.Err()}
			return
		}
		input := remotestate.RepositoryInput{Repository: target.repository, Path: target.path}
		input.Status, input.Tracking, input.Err = scans.of(target.path, identity.PublishedAt)
		inputs[index] = input
		progress.repositoryComplete(target.repository, input.Err)
	})
	if err := ctx.Err(); err != nil {
		return remotestate.Snapshot{}, err
	}
	listed := make(map[string]bool, len(targets))
	for _, target := range targets {
		listed[target.path] = true
	}
	scans.keepOnly(listed)
	// No OwnerState filter: this snapshot is a fleet-audit artifact, and
	// abandoned worktrees (sessions that exited without cleanup) are exactly
	// what cross-machine reconciliation needs to see. Filtering to "active"
	// here made `wb remote publish` under-report worktree counts on any
	// machine holding orphaned worktrees.
	progress.phase("inspecting worktrees")
	wts, err := worktrees.List(ctx, worktrees.ListOptions{
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
