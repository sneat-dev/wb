package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// landingLaneOwner resolves this process's landing-lane owner identity from
// the WB session registry (see internal/session), the same registry every
// other guard in WB reads liveness from — never a bare PID.
//
// An unregistered process (no live session ever declared itself) returns a
// zero Owner: the landing-lane guard is a deliberate no-op in that case,
// exactly as it is for the many direct orchestrate callers and tests that
// never populate LaneGuardRequest at all. WB does not invent an owner for a
// caller that never declared one.
func landingLaneOwner(command string) landinglane.Owner {
	directory, err := sessionDirForRead()
	if err != nil {
		return landinglane.Owner{}
	}
	record, ok := session.ResolveForProcess(directory, os.Getpid())
	if !ok {
		return landinglane.Owner{}
	}
	return landinglane.Owner{
		WBSessionID: record.WBSessionID,
		PID:         record.PID,
		Runtime:     record.Runtime,
		Model:       record.Model,
		Command:     command,
	}
}

// landingLaneGuardRequest builds the LaneGuardRequest for one landing
// command from the resolved session owner and the shared
// --take-over-lane/--lane-reason override flags.
func landingLaneGuardRequest(command, reason string, takeOver bool) orchestrate.LaneGuardRequest {
	return orchestrate.LaneGuardRequest{
		Owner:          landingLaneOwner(command),
		TakeOver:       takeOver,
		TakeoverReason: reason,
	}
}

// releaseWorktreeMergeLane frees this session's landing-lane ownership once a
// worktree-merge receipt reaches a status that no longer needs it — see
// orchestrate.WorktreeMergeLaneReleasable. It is a best-effort courtesy, like
// releaseLandingLane in internal/orchestrate: a failure here never overrides
// the caller's real result, and it is a deliberate no-op when the receipt
// carries no repository/target (an error returned before either was known)
// or this process never resolved a live session — the lane it might still
// hold then clears itself once its heartbeat goes stale.
func releaseWorktreeMergeLane(receipt orchestrate.WorktreeMergeReceipt) {
	if receipt.Repository == "" || receipt.Target == "" || !orchestrate.WorktreeMergeLaneReleasable(receipt.Status) {
		return
	}
	owner := landingLaneOwner("")
	if owner.WBSessionID == "" {
		return
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return
	}
	_ = landinglane.Release(home, receipt.Repository, receipt.Target, owner.WBSessionID)
}

// addLandingLaneTakeoverFlag adds the one sanctioned override for a refused
// landing lane: --take-over-lane, which requires --lane-reason (already present
// or added by the caller) to be non-empty. See internal/landinglane.
func addLandingLaneTakeoverFlag(command *cobra.Command, takeOver *bool) {
	command.Flags().BoolVar(takeOver, "take-over-lane", false,
		"override a refused landing lane held by a different WB session; requires --lane-reason <text>, which is recorded on the lane and the receipt")
}
