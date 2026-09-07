package orchestrate

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// LaneGuardRequest optionally names the acquiring WB session for the
// landing-lane ownership guard (see internal/landinglane). A zero value
// (empty Owner.WBSessionID) skips the guard entirely: every existing direct
// caller of PrepareWorktreeMerge, LandWorktreeMerge, ResumeWorktreeMerge, or
// LandPullRequest that never populates this field — including every test in
// this package — keeps working exactly as before. Only `cmd/wb`, which knows
// the calling session's identity, populates it.
//
// On 2026-09-07 two live WB sessions landed on sneat-dev/wb main
// concurrently: main advanced under a published candidate four times in one
// session, each time costing a re-prepare or stranding a receipt. The
// working agreement is one landing owner per (repository, target branch);
// this guard is the mechanical enforcement of it.
type LaneGuardRequest struct {
	Owner          landinglane.Owner
	TakeOver       bool
	TakeoverReason string
	// StaleAfter overrides how long a prior owner's heartbeat may go
	// unrefreshed before it is taken over automatically. Zero uses
	// landinglane.DefaultStaleAfter.
	StaleAfter time.Duration
}

// acquireLandingLane admits request.Owner as the (repository, target) lane
// owner, refusing a different live owner and naming it in the returned
// *landinglane.ConflictError. It is a deliberate no-op — returning a zero
// Record and a nil error — when request.Owner.WBSessionID is empty.
func acquireLandingLane(projectsRoot, repository, target string, request LaneGuardRequest) (landinglane.Record, error) {
	if strings.TrimSpace(request.Owner.WBSessionID) == "" {
		return landinglane.Record{}, nil
	}
	home, err := wbhome.EnsureRoot(projectsRoot)
	if err != nil {
		return landinglane.Record{}, err
	}
	return landinglane.Acquire(home, landinglane.AcquireRequest{
		Repository:     repository,
		Target:         target,
		Self:           request.Owner,
		SessionDir:     filepath.Join(home, session.DirName),
		StaleAfter:     request.StaleAfter,
		TakeOver:       request.TakeOver,
		TakeoverReason: request.TakeoverReason,
	})
}

// releaseLandingLane frees wbSessionID's ownership of the (repository,
// target) lane, when it holds one. Callers use it once their receipt reaches
// a terminal state, or once they exit without an active (resumable) receipt.
// It is a no-op when wbSessionID is empty, so a caller that never acquired a
// lane never needs to guard the release call too.
func releaseLandingLane(projectsRoot, repository, target, wbSessionID string) error {
	if strings.TrimSpace(wbSessionID) == "" {
		return nil
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		// WB's home not existing yet means nothing could have acquired a
		// lane either; that is not a release failure.
		return nil
	}
	return landinglane.Release(home, repository, target, wbSessionID)
}

// WorktreeMergeLaneReleasable reports whether a worktree-merge receipt in
// this status should free its landing lane immediately. A resumable,
// still-live receipt (preparing, prepared, published and awaiting merge, or
// awaiting checks) keeps the lane so a different session cannot start
// landing onto the same target underneath a batch this session is still
// driving; every other status — landed, failed, or complete — releases it.
func WorktreeMergeLaneReleasable(status WorktreeMergeStatus) bool {
	switch status {
	case WorktreeMergePreparing, WorktreeMergePrepared, WorktreeMergePublished, WorktreeMergeChecksPending:
		return false
	default:
		return true
	}
}
