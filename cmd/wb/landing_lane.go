package main

import (
	"os"

	"github.com/sneat-dev/wb/internal/landingcontext"
	"github.com/sneat-dev/wb/internal/orchestrate"
)

// landingLaneGuardRequest builds the LaneGuardRequest for one landing
// command from the resolved session owner and the shared
// --take-over-lane/--lane-reason override flags.
func landingLaneGuardRequest(inv *invocation, command, reason string, takeOver bool) orchestrate.LaneGuardRequest {
	return landingcontext.LaneRequest(inv.projectsRoot, command, reason, takeOver, os.Getpid())
}

// releaseWorktreeMergeLane frees this session's landing-lane ownership once a
// worktree-merge receipt reaches a status that no longer needs it — see
// orchestrate.WorktreeMergeLaneReleasable. It is a best-effort courtesy, like
// releaseLandingLane in internal/orchestrate: a failure here never overrides
// the caller's real result, and it is a deliberate no-op when the receipt
// carries no repository/target (an error returned before either was known)
// or this process never resolved a live session — the lane it might still
// hold then clears itself once its heartbeat goes stale.
func releaseWorktreeMergeLane(inv *invocation, receipt orchestrate.WorktreeMergeReceipt) {
	landingcontext.ReleaseWorktreeLane(inv.projectsRoot, receipt, os.Getpid())
}
