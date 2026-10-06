package main

import (
	"io"
	"time"

	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/sneat-dev/wb/internal/worktreerun"
)

const autoClaimStale = 24 * time.Hour
const exitNonZeroOnReleaseLeak = false

type autoClaimResult = worktreerun.RemoteClaimOutcome
type autoReleaseResult = remoterun.ReleaseAdvisory

func tryAutoClaim(deps remoteDeps, root, task string, stale time.Duration, out io.Writer) autoClaimResult {
	return remoteService(deps).AutoClaim(root, task, stale, out)
}
func tryAutoRelease(deps remoteDeps, root, task string, out io.Writer) autoReleaseResult {
	return remoteService(deps).AutoRelease(root, task, out)
}
func skippedAutoRelease(out io.Writer, detail string) autoReleaseResult {
	return remoterun.SkippedAutoRelease(out, detail)
}
func failedAutoRelease(out io.Writer, task, detail string) autoReleaseResult {
	return remoterun.FailedAutoRelease(out, task, detail)
}

var (
	releaseRemoteClaim = func(projectsRoot, task string, out io.Writer) autoReleaseResult {
		return tryAutoRelease(defaultRemoteDeps(), projectsRoot, task, out)
	}
	claimRemoteTask = func(noClaim bool, projectsRoot, task string, out io.Writer) autoClaimResult {
		return worktreeCreateAutoClaim(defaultRemoteDeps(), noClaim, projectsRoot, task, out)
	}
)

func worktreeCreateAutoClaim(deps remoteDeps, noClaim bool, projectsRoot, task string, out io.Writer) autoClaimResult {
	if noClaim {
		return autoClaimResult{Outcome: "disabled"}
	}
	return tryAutoClaim(deps, projectsRoot, task, autoClaimStale, out)
}
